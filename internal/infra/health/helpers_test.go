package health_test

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// freePort занимает свободный порт и сразу отпускает его, отдавая адрес.
//
// Гонка тут теоретически возможна, но выбор фиксированного номера
// столкнул бы параллельные тесты гарантированно.
func freePort(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("занять порт не удалось: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("освободить порт не удалось: %v", err)
	}
	return addr
}

// waitReady дожидается, пока сервер начнёт отвечать.
func waitReady(t *testing.T, url string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // ожидание локального сервера
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("сервер на %s так и не ответил", url)
}
