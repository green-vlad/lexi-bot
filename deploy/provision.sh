#!/usr/bin/env bash
#
# Подготовка чистого сервера под lexi-bot: Docker, пользователь deploy,
# файрвол, автообновления, ротация логов, swap.
#
# Запускать от root на свежей Ubuntu 24.04 LTS:
#
#   scp deploy/provision.sh root@<адрес>:/tmp/
#   ssh root@<адрес> bash /tmp/provision.sh
#
# Скрипт идемпотентен: повторный запуск ничего не ломает и говорит, что
# уже сделано. Это важнее краткости — к нему возвращаются при переезде
# на другую машину и при разборе того, что на сервере вообще настроено.
set -euo pipefail

# Куда кладём compose-файл и .env.
readonly APP_DIR=/opt/lexi-bot
# От чьего имени работает деплой. Не root: SSH-ключ из GitHub Actions
# не должен открывать машину целиком.
readonly APP_USER=deploy
# Размер файла подкачки. Два гигабайта памяти — не то чтобы много, и swap
# здесь дешёвая страховка от разового всплеска при миграции или pg_dump.
readonly SWAP_SIZE=2G

main() {
	require_root
	install_packages
	install_docker
	configure_docker_logs
	create_user
	create_app_dir
	configure_swap
	configure_firewall
	configure_updates
	harden_ssh
	summary
}

# --- Шаги -------------------------------------------------------------

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		fail "скрипт запускается от root"
	fi
	if ! grep -q "Ubuntu" /etc/os-release 2>/dev/null; then
		warn "система не Ubuntu — дальше на ваш страх и риск"
	fi
}

install_packages() {
	step "базовые пакеты"

	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq
	apt-get install -y -qq \
		ca-certificates curl gnupg ufw unattended-upgrades apt-listchanges
}

# install_docker ставит Docker из репозитория самого Docker, а не из Ubuntu:
# в дистрибутиве лежит docker.io, который отстаёт на версии и не приносит
# плагин compose.
install_docker() {
	if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
		skip "Docker уже стоит: $(docker --version)"
		return
	fi

	step "Docker и плагин compose"

	install -m 0755 -d /etc/apt/keyrings
	curl -fsSL https://download.docker.com/linux/ubuntu/gpg |
		gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
	chmod a+r /etc/apt/keyrings/docker.gpg

	local codename
	codename=$(. /etc/os-release && echo "$VERSION_CODENAME")
	cat >/etc/apt/sources.list.d/docker.list <<-EOF
		deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $codename stable
	EOF

	apt-get update -qq
	apt-get install -y -qq \
		docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

	systemctl enable --now docker
}

# configure_docker_logs ограничивает журналы контейнеров.
#
# Без этого json-file растёт без предела и однажды доедает диск — отказ,
# который выглядит как «бот молчит», а чинится не там, где ищут. Настройка
# демона, а не compose-файла: она накрывает и контейнеры, поднятые руками.
configure_docker_logs() {
	local config=/etc/docker/daemon.json

	if [ -f "$config" ] && grep -q "max-size" "$config"; then
		skip "ротация docker-логов уже настроена"
		return
	fi
	if [ -f "$config" ]; then
		warn "$config уже существует — правьте вручную, не перетираю"
		return
	fi

	step "ротация docker-логов"
	install -m 0644 /dev/null "$config"
	cat >"$config" <<-'EOF'
		{
		  "log-driver": "json-file",
		  "log-opts": {
		    "max-size": "10m",
		    "max-file": "3"
		  }
		}
	EOF
	systemctl restart docker
}

create_user() {
	if id "$APP_USER" >/dev/null 2>&1; then
		skip "пользователь $APP_USER уже есть"
	else
		step "пользователь $APP_USER"
		adduser --disabled-password --gecos "" "$APP_USER"
	fi

	usermod -aG docker "$APP_USER"

	# Ключи достаются от root: администратор уже вошёл этой машиной, значит
	# ключ у него рабочий, и переносить его руками — лишний шаг, на котором
	# можно ошибиться и закрыть себе дверь.
	local root_keys=/root/.ssh/authorized_keys
	local user_keys="/home/$APP_USER/.ssh/authorized_keys"

	if [ -s "$root_keys" ] && [ ! -s "$user_keys" ]; then
		step "перенос SSH-ключей к $APP_USER"
		install -d -m 700 -o "$APP_USER" -g "$APP_USER" "/home/$APP_USER/.ssh"
		install -m 600 -o "$APP_USER" -g "$APP_USER" "$root_keys" "$user_keys"
	fi
}

create_app_dir() {
	if [ -d "$APP_DIR" ]; then
		skip "каталог $APP_DIR уже есть"
		return
	fi

	step "каталог $APP_DIR"
	install -d -m 755 -o "$APP_USER" -g "$APP_USER" "$APP_DIR"
}

configure_swap() {
	if swapon --show | grep -q .; then
		skip "swap уже подключён"
		return
	fi

	step "swap на $SWAP_SIZE"
	fallocate -l "$SWAP_SIZE" /swapfile
	chmod 600 /swapfile
	mkswap /swapfile >/dev/null
	swapon /swapfile
	grep -q "^/swapfile" /etc/fstab || echo "/swapfile none swap sw 0 0" >>/etc/fstab

	# Подкачка как страховка, а не как рабочий режим: с 10 база остаётся
	# в памяти, пока её действительно не начнёт не хватать.
	sysctl -q -w vm.swappiness=10
	grep -q "^vm.swappiness" /etc/sysctl.conf || echo "vm.swappiness=10" >>/etc/sysctl.conf
}

# configure_firewall закрывает всё, кроме SSH.
#
# Порт берётся из настроек самого sshd, а не из константы: на машине
# с нестандартным портом жёсткая «22» заперла бы администратора снаружи
# ровно в момент включения файрвола.
configure_firewall() {
	local port
	port=$(sshd -T 2>/dev/null | awk '/^port /{print $2; exit}')
	port=${port:-22}

	step "ufw: наружу только $port/tcp"

	ufw --force reset >/dev/null
	ufw default deny incoming >/dev/null
	ufw default allow outgoing >/dev/null
	ufw allow "$port/tcp" comment "SSH" >/dev/null
	ufw --force enable >/dev/null
}

configure_updates() {
	step "автообновления безопасности"

	cat >/etc/apt/apt.conf.d/20auto-upgrades <<-'EOF'
		APT::Periodic::Update-Package-Lists "1";
		APT::Periodic::Unattended-Upgrade "1";
	EOF

	# Перезагрузка по ночам, когда занятий заведомо нет. Контейнеры поднимутся
	# сами: у обоих сервисов restart: unless-stopped.
	cat >/etc/apt/apt.conf.d/51lexi-reboot <<-'EOF'
		Unattended-Upgrade::Automatic-Reboot "true";
		Unattended-Upgrade::Automatic-Reboot-Time "04:30";
	EOF

	systemctl enable --now unattended-upgrades >/dev/null 2>&1 || true
}

# harden_ssh выключает вход по паролю.
#
# Только если ключ уже на месте: скрипт, запирающий администратора снаружи,
# хуже, чем сервер с включённой парольной аутентификацией.
harden_ssh() {
	local user_keys="/home/$APP_USER/.ssh/authorized_keys"

	if [ ! -s "$user_keys" ]; then
		warn "у $APP_USER нет SSH-ключа — вход по паролю оставлен включённым"
		warn "положите ключ в $user_keys и перезапустите скрипт"
		return
	fi

	step "SSH: вход только по ключу"
	install -d -m 755 /etc/ssh/sshd_config.d
	cat >/etc/ssh/sshd_config.d/99-lexi.conf <<-'EOF'
		PasswordAuthentication no
		KbdInteractiveAuthentication no
		PermitRootLogin prohibit-password
	EOF

	if sshd -t; then
		systemctl reload ssh 2>/dev/null || systemctl reload sshd
	else
		rm -f /etc/ssh/sshd_config.d/99-lexi.conf
		fail "конфигурация sshd не прошла проверку, изменения откачены"
	fi
}

summary() {
	echo
	echo "Готово. Дальше — руками:"
	echo
	echo "  1. Скопировать на сервер compose-файл:"
	echo "       scp docker-compose.prod.yml $APP_USER@<адрес>:$APP_DIR/"
	echo "  2. Создать $APP_DIR/.env по образцу .env.prod.example (права 600)."
	echo "  3. Войти в GHCR, если пакет приватный:"
	echo "       echo <токен> | docker login ghcr.io -u <логин> --password-stdin"
	echo "  4. Поднять:"
	echo "       cd $APP_DIR && docker compose -f docker-compose.prod.yml up -d"
	echo "  5. Проверить: curl -fsS http://127.0.0.1:8080/healthz"
	echo
	echo "Снаружи должен быть виден только SSH: nmap -Pn <адрес>"
	echo
}

# --- Вывод ------------------------------------------------------------

step() { printf '\033[1m==>\033[0m %s\n' "$1"; }
skip() { printf '    %s\n' "$1"; }
warn() { printf '\033[33m !!\033[0m %s\n' "$1" >&2; }
fail() {
	printf '\033[31m !!\033[0m %s\n' "$1" >&2
	exit 1
}

main "$@"
