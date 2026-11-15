package stages

import (
	"context"

	"github.com/fr33dman/yandex-station-picklock/pkg/quasarsound"
	"github.com/fr33dman/yandex-station-picklock/pkg/scenario"
)

func ConnectStationToWifi(ctx context.Context) error {
	s, err := session(ctx)
	if err != nil {
		return err
	}
	// Сигнал и цикл повторов готовит SDK: сценарию не нужны собственные
	// вызовы quasarsound и проигрывателя.
	config := quasarsound.Config{SSID: s.ssid, Password: s.password, Hidden: s.hidden, Token: "offline"}
	s.password = "" // Пароль остаётся только в подготовленном сигнале и затирается SDK.
	return scenario.PairWiFi(s.UI, config, "Колонка подключилась к изолированной Wi-Fi сети?")(ctx)
}
