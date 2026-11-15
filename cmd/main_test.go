package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

// TestInteractiveDefaultsAndStandaloneFlags проверяет режим по умолчанию и ограничения флагов OTA.
func TestInteractiveDefaultsAndStandaloneFlags(t *testing.T) {
	o, err := parseOptions(nil) // Получаем проверенные настройки CLI.
	if err != nil {
		t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
	}
	if o.backendOnly || len(scenarios(o)) != 1 { // Проверяем количество доступных элементов.
		t.Fatal("default mode is not the scenario catalog") // Немедленно завершаем тест при нарушении условия.
	}
	if _, err := parseOptions([]string{"-firmware", "image.zip"}); err == nil { // Получаем проверенные настройки CLI.
		t.Fatal("standalone flags silently bypassed the UI") // Немедленно завершаем тест при нарушении условия.
	}
	o, err = parseOptions([]string{"-backend", "-listen", "127.0.0.1:8080"}) // Получаем проверенные настройки CLI.
	if err != nil || !o.backendOnly {
		t.Fatalf("standalone mode: %v", err) // Завершаем тест с диагностикой полученных значений.
	}
	if _, err := listenerConfig(options{certificates: paths{"cert.pem"}}); err == nil { // Собираем параметры HTTP/TLS-слушателей.
		t.Fatal("unpaired TLS certificate was accepted") // Немедленно завершаем тест при нарушении условия.
	}
}

// canceledInteraction имитирует отмену любого обращения к пользователю.
type canceledInteraction struct{}

// Ask возвращает отмену вместо текстового ответа.
func (canceledInteraction) Ask(context.Context, execution.Question) (string, error) {
	return "", context.Canceled
}

// Confirm возвращает отказ с ошибкой отмены.
func (canceledInteraction) Confirm(context.Context, string) (bool, error) {
	return false, context.Canceled
}

// Play возвращает отмену без воспроизведения звука.
func (canceledInteraction) Play(context.Context, []byte) error {
	return context.Canceled
}

// TestCatalogConstructionIsLazyAndSetupCanBeCanceled проверяет отложенную сборку и отмену подготовки.
func TestCatalogConstructionIsLazyAndSetupCanBeCanceled(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "not-created")                    // Собираем путь с разделителями текущей ОС.
	entries := scenarios(options{backups: directory, adb: "nonexistent-adb"}) // Собираем каталог фабрик сценариев.
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {        // Проверяем, появился ли каталог до фактического запуска.
		t.Fatal("listing the catalog created a backup directory") // Немедленно завершаем тест при нарушении условия.
	}
	if len(entries) != 1 || entries[0].Name == "" || entries[0].Description == "" { // Проверяем количество доступных элементов.
		t.Fatal("missing catalog metadata") // Немедленно завершаем тест при нарушении условия.
	}
	if _, err := entries[0].Build(t.Context(), canceledInteraction{}); !errors.Is(err, context.Canceled) { // Вызываем фабрику выбранного сценария с тестовым взаимодействием.
		t.Fatalf("setup ignored cancellation: %v", err) // Завершаем тест с диагностикой полученных значений.
	}
}

// TestHomePaths проверяет раскрытие домашнего каталога в путях.
func TestHomePaths(t *testing.T) {
	got, err := absolutePath("~/.ssh/key", "/users/example") // Раскрываем домашний каталог и нормализуем путь.
	if err != nil || got != "/users/example/.ssh/key" {
		t.Fatalf("home path: %s %v", got, err) // Завершаем тест с диагностикой полученных значений.
	}
}
