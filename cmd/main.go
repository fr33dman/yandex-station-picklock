package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/fr33dman/yandex-station-picklock/internal/ui"
	"github.com/fr33dman/yandex-station-picklock/pkg/backend/ota"
	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

// main запускает приложение и выставляет код ошибки при сбое; обычная отмена допустима.
func main() {
	if err := run(); err != nil && !execution.IsCancellation(err) { // Запускаем выбранный режим приложения.
		slog.Error("Picklocker stopped", "error", err) // Записываем причину аварийного завершения.
		os.Exit(1)                                     // Завершаем процесс с кодом ошибки.
	}
}

// paths накапливает пути из повторяемых флагов сертификатов и ключей.
type paths []string

// String возвращает строковое представление путей для пакета flag.
func (p *paths) String() string {
	return fmt.Sprint([]string(*p)) // Формируем строковое представление путей.
}

// Set добавляет очередное значение повторяемого флага.
func (p *paths) Set(value string) error {
	*p = append(*p, value) // Сохраняем очередной путь повторяемого флага.
	return nil
}

// options хранит параметры CLI для сценариев и отдельного бэкенда.
type options struct {
	backendOnly                                            bool        // Запуск только сервера, без выбора сценария.
	listen, httpsListen, publicURL, adb, identity, backups string      // Адреса HTTP/HTTPS, URL OTA и пути ADB, SSH-ключа и бэкапов.
	certificates, keys                                     paths       // Пары сертификатов и ключей в одинаковом порядке.
	firmware, digest                                       string      // Локальный OTA-образ и его ожидаемый SHA-256.
	release                                                ota.Release // Метаданные OTA для отдельного бэкенда.
}

// parseOptions разбирает флаги и проверяет совместимость выбранных параметров.
func parseOptions(args []string) (options, error) {
	var o options                                                                                                    // Параметры текущего запуска CLI.
	flags := flag.NewFlagSet("picklocker", flag.ContinueOnError)                                                     // Создаём отдельный набор флагов CLI.
	flags.BoolVar(&o.backendOnly, "backend", false, "run only the HTTP backend, without fullscreen UI")              // Флаг запуска одного бэкенда без UI.
	flags.StringVar(&o.listen, "listen", "0.0.0.0:8080", "HTTP listen address (or internal ingress address)")        // Адрес HTTP-слушателя локального бэкенда.
	flags.StringVar(&o.httpsListen, "https-listen", "", "HTTPS listen address; optional behind a TLS reverse proxy") // Адрес прямого HTTPS-слушателя, если он нужен.
	flags.StringVar(&o.publicURL, "public-url", "", "local HTTP ingress URL for OTA; asked interactively if empty")  // URL, по которому станция скачает OTA.
	flags.StringVar(&o.adb, "adb", "adb", "path to the ADB executable")                                              // Путь к исполняемому файлу ADB.
	flags.StringVar(&o.identity, "ssh-key", "", "SSH private key path; asked interactively if empty")                // Путь к закрытому SSH-ключу станции.
	flags.StringVar(&o.backups, "backup-dir", "", "persistent backup directory (default ~/.picklocker/backups)")     // Каталог постоянного хранения бэкапов.
	flags.Var(&o.certificates, "tls-cert", "TLS certificate chain; repeat for different domains")                    // Повторяемый путь к цепочке TLS-сертификатов.
	flags.Var(&o.keys, "tls-key", "matching TLS private key; repeat in the same order")                              // Повторяемый путь к ключу в порядке сертификатов.
	flags.StringVar(&o.firmware, "firmware", "", "standalone backend OTA image; requires -backend")                  // Локальный OTA-образ для отдельного бэкенда.
	flags.StringVar(&o.digest, "firmware-sha256", "", "independently verified image SHA-256")                        // Ожидаемый SHA-256 OTA-образа.
	flags.StringVar(&o.release.DownloadURL, "firmware-url", "", "standalone local OTA image URL")                    // URL OTA-образа в ответе станции.
	flags.StringVar(&o.release.SourceVersion, "source-version", "", "standalone OTA source version")                 // Версия прошивки, для которой доступно OTA.
	flags.StringVar(&o.release.Version, "target-version", "", "standalone OTA target version")                       // Версия предлагаемой прошивки.
	flags.StringVar(&o.release.Platform, "platform", "", "standalone OTA platform")                                  // Аппаратная платформа предлагаемого OTA.
	flags.StringVar(&o.release.UpdateID, "update-id", "", "standalone OTA release ID")                               // Идентификатор предлагаемого обновления.
	if err := flags.Parse(args); err != nil {                                                                        // Разбираем переданные аргументы CLI.
		return o, err
	}
	if flags.NArg() != 0 { // Проверяем отсутствие позиционных аргументов.
		return o, errors.New("unexpected positional arguments") // Отклоняем неподдерживаемые позиционные аргументы.
	}
	if !o.backendOnly && (o.firmware != "" || o.digest != "" || o.release != (ota.Release{})) {
		return o, errors.New("standalone OTA flags require -backend") // Не допускаем ручную настройку OTA в режиме сценария.
	}
	return o, nil
}

// run выбирает режим, обрабатывает сигналы и направляет логи в полноэкранный UI.
func run() error {
	o, err := parseOptions(os.Args[1:]) // Получаем проверенные настройки CLI.
	if errors.Is(err, flag.ErrHelp) {   // Показ справки не считается ошибкой запуска.
		return nil
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP) // Связываем отмену приложения с сигналами ОС.
	defer stop()                                                                                           // Освобождаем обработчики сигналов при выходе.
	if o.backendOnly {
		return serveBackend(ctx, o) // Запускаем отдельный сервер без UI.
	}
	app := ui.New() // Создаём полноэкранный интерфейс.

	previous := slog.Default()                                                               // Сохраняем текущий структурированный логгер.
	previousOutput, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix() // Сохраняем вывод и оформление стандартного логгера.
	slog.SetDefault(slog.New(slog.NewTextHandler(app, nil)))                                 // Направляем структурированные и стандартные логи в UI.
	defer func() {                                                                           // При выходе восстанавливаем оба исходных логгера.
		slog.SetDefault(previous)     // Восстанавливаем логгер, работавший до UI.
		log.SetOutput(previousOutput) // Восстанавливаем исходный вывод логов.
		log.SetFlags(previousFlags)   // Восстанавливаем оформление логов.
		log.SetPrefix(previousPrefix) // Восстанавливаем префикс логов.
	}()
	return app.Run(ctx, scenarios(o)) // Запускаем UI с доступными сценариями.
}
