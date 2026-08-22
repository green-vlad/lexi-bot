package telegram

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"time"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/infra/logger"
	"lexi-bot/internal/usecase/port"
)

// Middleware оборачивает хендлер, добавляя к обработке общее поведение.
type Middleware func(next port.UpdateHandler) port.UpdateHandler

// Recover не даёт панике уронить процесс.
//
// Бот обрабатывает апдейты в отдельных горутинах, и паника в одной из них
// снимает всё приложение — вместе с сессиями остальных пользователей. Поэтому
// паника здесь превращается в ошибку, а пользователь получает извинение
// вместо тишины: молчащий бот выглядит сломанным сильнее, чем честное
// «что-то пошло не так».
func Recover(messenger port.Messenger, catalog port.Catalog, log *slog.Logger) Middleware {
	return func(next port.UpdateHandler) port.UpdateHandler {
		return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) (err error) {
			defer func() {
				reason := recover()
				if reason == nil {
					return
				}

				logger.FromContext(ctx, log).Error("паника при обработке апдейта",
					slog.Any("reason", reason),
					slog.String("stack", string(debug.Stack())))

				apologize(ctx, messenger, catalog, u, log)
				err = errPanic
			}()

			return next.Handle(ctx, u)
		})
	}
}

// errPanic возвращается вместо паники: транспорт залогирует его как обычную
// ошибку обработки, а вызывающий код не будет разбираться в панике.
var errPanic = errors.New("обработка прервана паникой")

// apologize сообщает пользователю, что запрос не удался.
//
// Ошибки отправки здесь только логируются: мы уже в аварийном пути, и падать
// в нём второй раз бессмысленно.
func apologize(ctx context.Context, messenger port.Messenger, catalog port.Catalog, u *port.Update, log *slog.Logger) {
	if messenger == nil || u.Chat == 0 {
		return
	}

	text := "Что-то пошло не так. Попробуйте ещё раз через минуту."
	if localizer, ok := LocalizerFrom(ctx); ok {
		if translated, err := localizer.T("common.error", nil); err == nil {
			text = translated
		}
	} else if catalog != nil {
		if translated, err := catalog.For(user.DefaultUILang).T("common.error", nil); err == nil {
			text = translated
		}
	}

	if _, err := messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text}); err != nil {
		logger.FromContext(ctx, log).Error("не удалось извиниться перед пользователем",
			slog.Any("error", err))
	}
}

// Logging пишет в лог начало и конец обработки.
//
// Идентификатор апдейта кладётся в контекст: по нему потом собирается вся
// цепочка обработки одного сообщения, включая записи из сценариев и
// репозиториев, которые про Telegram ничего не знают.
func Logging(log *slog.Logger) Middleware {
	return func(next port.UpdateHandler) port.UpdateHandler {
		return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
			ctx = logger.WithUpdateID(ctx, u.ID)
			if u.Sender.TelegramID != 0 {
				// Внутреннего идентификатора здесь ещё нет — его добавит
				// Identify, когда найдёт пользователя в базе.
				ctx = logger.WithTelegramID(ctx, int64(u.Sender.TelegramID))
			}

			started := time.Now()
			err := next.Handle(ctx, u)

			attrs := []any{
				slog.String("kind", kind(u)),
				slog.Duration("elapsed", time.Since(started)),
			}
			if u.Command != "" {
				attrs = append(attrs, slog.String("command", u.Command))
			}

			entry := logger.FromContext(ctx, log)
			if err != nil {
				entry.Error("апдейт обработан с ошибкой", append(attrs, slog.Any("error", err))...)
				return err
			}
			entry.Debug("апдейт обработан", attrs...)
			return nil
		})
	}
}

func kind(u *port.Update) string {
	switch {
	case u.Callback != nil:
		return "callback"
	case u.IsCommand():
		return "command"
	case u.Document != nil:
		return "document"
	default:
		return "text"
	}
}

// AnswerCallbacks снимает «часики» с нажатой кнопки.
//
// Telegram ждёт ответа на каждое нажатие и крутит индикатор, пока его нет.
// Отвечаем сразу, до обработки: сама обработка может сходить в базу
// несколько раз, и всё это время кнопка выглядела бы зависшей. Ошибка
// ответа только логируется — нажатие могло прийти от сообщения, которого
// уже нет, и портить из-за этого обработку незачем.
func AnswerCallbacks(messenger port.Messenger, log *slog.Logger) Middleware {
	return func(next port.UpdateHandler) port.UpdateHandler {
		return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
			if u.Callback == nil {
				return next.Handle(ctx, u)
			}

			if err := messenger.AnswerCallback(ctx, port.CallbackAnswer{ID: u.Callback.ID}); err != nil {
				logger.FromContext(ctx, log).Warn("не удалось ответить на нажатие кнопки",
					slog.Any("error", err))
			}
			return next.Handle(ctx, u)
		})
	}
}

// Identify находит пользователя, а незнакомого заводит.
//
// Регистрация происходит на любом апдейте, а не только на /start: человек
// мог начать с любой команды, а нажатие кнопки под чужим сообщением или
// пересланная команда вообще минуют онбординг. Пользователь без записи
// в базе не сможет ничего сохранить, и разбираться с этим в каждом сценарии
// было бы утомительно.
func Identify(users port.UserRepo, log *slog.Logger) Middleware {
	return func(next port.UpdateHandler) port.UpdateHandler {
		return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
			if u.Sender.TelegramID == 0 {
				return next.Handle(ctx, u)
			}

			found, err := users.ByTelegramID(ctx, u.Sender.TelegramID)
			switch {
			case err == nil:
				// Имя в Telegram могло смениться. Пишем только когда оно
				// действительно другое: иначе каждый апдейт превращался бы
				// в запись в базу.
				if found.Username != normalized(u.Sender.Username) {
					found, err = ensure(ctx, users, u, found.UILang)
					if err != nil {
						return err
					}
				}
			case errors.Is(err, port.ErrNotFound):
				lang, _ := user.MatchUILang(u.Sender.LanguageCode)
				found, err = ensure(ctx, users, u, lang)
				if err != nil {
					return err
				}
				logger.FromContext(ctx, log).Info("зарегистрирован новый пользователь",
					slog.Int64("user_id", int64(found.ID)),
					slog.String("ui_lang", found.UILang.String()))

			default:
				return err
			}

			// Внутренний идентификатор попадает в контекст логгера: дальше
			// его увидят и сценарии, и репозитории, которые про Telegram
			// ничего не знают.
			ctx = logger.WithUserID(ctx, int64(found.ID))
			return next.Handle(withUser(ctx, found), u)
		})
	}
}

func ensure(ctx context.Context, users port.UserRepo, u *port.Update, lang user.UILang) (user.User, error) {
	wanted, err := user.NewUser(u.Sender.TelegramID, u.Sender.Username, lang)
	if err != nil {
		// Имя пользователя приходит из Telegram и теоретически может
		// не пройти нашу проверку. Терять из-за этого человека нельзя:
		// заводим его без имени.
		wanted, err = user.NewUser(u.Sender.TelegramID, "", lang)
		if err != nil {
			return user.User{}, err
		}
	}

	saved, _, err := users.Ensure(ctx, wanted)
	return saved, err
}

// normalized повторяет нормализацию имени из домена, чтобы сравнение
// «изменилось ли имя» не срабатывало на собачке и пробелах.
func normalized(username string) string {
	u, err := user.NewUser(1, username, user.DefaultUILang)
	if err != nil {
		return ""
	}
	return u.Username
}

// Localize подставляет локализатор языка пользователя.
//
// Для незнакомого пользователя язык берётся из настроек его клиента
// Telegram: первое сообщение должно прийти на понятном языке, а не на том,
// который мы объявили языком по умолчанию.
func Localize(catalog port.Catalog) Middleware {
	return func(next port.UpdateHandler) port.UpdateHandler {
		return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
			lang := user.DefaultUILang
			if known, ok := UserFrom(ctx); ok {
				lang = known.UILang
			} else if matched, ok := user.MatchUILang(u.Sender.LanguageCode); ok {
				lang = matched
			}

			return next.Handle(withLocalizer(ctx, catalog.For(lang)), u)
		})
	}
}
