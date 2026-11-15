package obtaining_root_access_via_offline_backend

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/fr33dman/yandex-station-picklock/internal/catalog/obtaining-root-access-via-offline-backend/stages"
	"github.com/fr33dman/yandex-station-picklock/internal/scenario"
	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
	sdk "github.com/fr33dman/yandex-station-picklock/pkg/scenario"
)

const Name = "OfflineBackendScenario"
const Description = "Настройка Wi-Fi и получение root через локальный offline backend (Station 1)"

// Scenario owns its backend lifetime; execution itself remains UI/transport independent.
type Scenario struct {
	scenario.Scenario
	session *stages.Session
	started atomic.Bool
}

// NewScenario creates independent execution state for each scenario run.
// Шаги собираются конструкторами SDK: критичный шаг задаёт NewStage, а снимаемые
// с шага флаги (Optional, BestEffort) остаются видимыми прямо в списке.
func NewScenario(config stages.Config, ui stages.Interaction, device stages.Device, backend stages.Backend) (*Scenario, error) {
	session, err := stages.NewSession(config, ui, device, backend)
	if err != nil {
		return nil, err
	}
	steps := []sdk.Stage{
		sdk.NewStage("fetch-info", "Получить модель и параметры изолированной Wi-Fi сети", stages.FetchInfo),
		// Точку доступа пользователь поднимает сам; шаг ждёт, пока сеть заработает.
		sdk.NewStage("wait-access-point", "Дождаться, пока пользователь включит точку доступа", stages.WaitAccessPoint),
		sdk.NewStage("connect-wifi", "Воспроизвести сигнал подключения к Wi-Fi", stages.ConnectStationToWifi),
		sdk.NewStage("update-firmware", "Отдать проверенную OTA-прошивку Station 1", stages.UpdateStationFirmware),
		sdk.NewStage("wait-update", "Дождаться обновления и подключения ADB", stages.WaitUpdate),
		sdk.NewStage("retrieve-boot", "Сохранить и проверить оригинальный boot", stages.RetrieveBoot),
		sdk.NewStage("prepare-boot", "Подготовить проверенный boot с ADB и временным Telnet", stages.PrepareBoot),
		sdk.NewStage("write-boot", "Записать boot и проверить полный readback", stages.WriteBoot),
		sdk.NewStage("reboot", "Перезагрузить колонку после подтверждения", stages.Reboot),
		sdk.NewStage("wait-reboot", "Проверить ADB и полный root через Telnet", stages.WaitReboot),
		sdk.NewStage("install-ssh", "Установить Dropbear и проверить вход по ключу", stages.AddDropbear),
		sdk.NewStage("disable-telnet", "Удалить Telnet из boot и остановить текущий сервис", stages.DisableTelnet),
		// Ограничение ADB сетью 192.168.0.0/16 — политика пользователя:
		// шаг можно пропустить, и его сбой не останавливает остальные.
		sdk.NewStage("restrict-adb", "Ограничить TCP/5555 сетью 192.168.0.0/16", stages.UpdateFirewall).Optional().BestEffort(),
	}
	pipeline, err := sdk.NewPipeline(steps...)
	if err != nil {
		return nil, err
	}
	return &Scenario{Scenario: scenario.Scenario{Name: Name, Description: Description, Pipeline: pipeline}, session: session}, nil
}

func (s *Scenario) BackupDirectory() string { return s.session.BackupDirectory() }

func (s *Scenario) Run(ctx context.Context) (err error) {
	if !s.started.CompareAndSwap(false, true) {
		return execution.ErrAlreadyStarted
	}
	defer func() {
		// Cleanup must run even when the user's execution context is canceled.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		err = errors.Join(err, s.session.Backend.Close(cleanup))
	}()
	return s.Scenario.Run(stages.WithSession(ctx, s.session))
}
