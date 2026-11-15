package main

import (
	"context"
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"strings"

	offline "github.com/fr33dman/yandex-station-picklock/internal/catalog/obtaining-root-access-via-offline-backend"
	"github.com/fr33dman/yandex-station-picklock/internal/catalog/obtaining-root-access-via-offline-backend/stages"
	"github.com/fr33dman/yandex-station-picklock/internal/ui"
	"github.com/fr33dman/yandex-station-picklock/pkg/backend"
	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
	"github.com/fr33dman/yandex-station-picklock/pkg/station"
)

// scenarios собирает каталог фабрик; конкретные сценарии подключаются только здесь.
func scenarios(o options) []ui.Entry {
	return []ui.Entry{{Name: offline.Name, Description: offline.Description, Build: func(ctx context.Context, interaction execution.Interaction) (*ui.Run, error) { // Фабрика собирает сценарий только по запросу UI.
		return buildOffline(ctx, o, interaction) // Собираем выбранный офлайн-сценарий.
	}}}
}

// buildOffline запрашивает настройки и собирает зависимости офлайн-сценария после подтверждения.
func buildOffline(ctx context.Context, o options, interaction execution.Interaction) (*ui.Run, error) {
	var err error // Ошибка текущей операции.
	if o.publicURL == "" {
		o.publicURL, err = interaction.Ask(ctx, execution.Question{Text: "HTTP URL локального ingress для OTA (например, http://192.168.1.10:8080). DNS и TLS ingress должны быть настроены для станции."}) // Запрашиваем недостающую настройку у пользователя.
		if err != nil {
			return nil, err
		}
	}
	home, err := os.UserHomeDir() // Получаем домашний каталог пользователя.
	if err != nil {
		return nil, err
	}
	if o.identity == "" {
		o.identity, err = interaction.Ask(ctx, execution.Question{Text: "Путь к закрытому SSH-ключу Ed25519. Публичный ключ должен лежать рядом с расширением .pub.", Default: filepath.Join(home, ".ssh", "id_ed25519")}) // Запрашиваем недостающую настройку у пользователя.
		if err != nil {
			return nil, err
		}
	}
	if o.backups == "" {
		o.backups = filepath.Join(home, ".picklocker", "backups") // Собираем путь с разделителями текущей ОС.
	}
	o.identity, err = absolutePath(o.identity, home) // Раскрываем домашний каталог и нормализуем путь.
	if err != nil {
		return nil, err
	}
	o.backups, err = absolutePath(o.backups, home) // Раскрываем домашний каталог и нормализуем путь.
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil { // Проверяем отмену или истечение срока контекста.
		return nil, err
	}
	device, err := station.NewClient(o.adb, o.identity) // Создаём клиент станции с ADB и SSH-ключом.
	if err != nil {
		return nil, err
	}
	listeners, err := listenerConfig(o) // Собираем параметры HTTP/TLS-слушателей.
	if err != nil {
		return nil, err
	}
	server, err := backend.NewController(listeners) // Создаём контроллер сменяемого бэкенда.
	if err != nil {
		return nil, err
	}
	scenario, err := offline.NewScenario(stages.Config{PublicURL: o.publicURL, BackupDir: o.backups, IdentityFile: o.identity}, interaction, device, server) // Собираем этапы сценария с его зависимостями.
	if err != nil {
		return nil, err
	}
	return &ui.Run{Pipeline: scenario.Pipeline, Execute: scenario.Run, Details: "Бэкапы: " + scenario.BackupDirectory()}, nil // Добавляем путь сохранённых бэкапов к результату.
}

// listenerConfig сопоставляет сертификаты с ключами и задаёт TLS не ниже версии 1.2.
func listenerConfig(o options) (backend.ListenerConfig, error) {
	config := backend.ListenerConfig{HTTPAddress: o.listen, HTTPSAddress: o.httpsListen} // Адреса слушателей до добавления параметров TLS.
	if len(o.certificates) != len(o.keys) {                                              // Каждому сертификату требуется ключ на той же позиции.
		return config, errors.New("each -tls-cert needs a matching -tls-key") // Отклоняем несовпадение числа сертификатов и ключей.
	}
	if len(o.certificates) > 0 { // TLS включается при наличии сертификатов.
		config.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		for i, certificate := range o.certificates {
			pair, err := tls.LoadX509KeyPair(certificate, o.keys[i]) // Загружаем сертификат вместе с соответствующим ключом.
			if err != nil {
				return config, err
			}
			config.TLSConfig.Certificates = append(config.TLSConfig.Certificates, pair) // Добавляем проверенную пару сертификата и ключа.
		}
		if config.HTTPSAddress == "" {
			config.HTTPSAddress = ":443"
		}
	}
	return config, nil
}

// absolutePath раскрывает домашний каталог и возвращает абсолютный путь.
func absolutePath(path, home string) (string, error) {
	if path == "" {
		return "", errors.New("empty filesystem path") // Отказываем при пустом пути.
	}
	if path == "~" {
		path = home
	} else if strings.HasPrefix(path, "~/") { // Распознаём путь относительно домашнего каталога.
		path = filepath.Join(home, path[2:]) // Собираем путь с разделителями текущей ОС.
	}
	return filepath.Abs(path) // Преобразуем путь в абсолютный.
}
