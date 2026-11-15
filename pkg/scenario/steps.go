package scenario

import (
	"context"
	"errors"
	"time"

	"github.com/fr33dman/yandex-station-picklock/pkg/quasarsound"
)

// Ask создаёт шаг текстового вопроса: ответ сохраняется в target.
func Ask(ui Interaction, question Question, target *string) Step {
	return func(ctx context.Context) error {
		value, err := ui.Ask(ctx, question) // Ждём ответ пользователя или отмену шага.
		if err != nil {
			return err
		}
		*target = value // Передаём ответ следующему шагу сценария.
		return nil
	}
}

// Confirm создаёт шаг явного согласия: отказ завершает шаг ошибкой.
func Confirm(ui Interaction, text string) Step {
	return func(ctx context.Context) error {
		ok, err := ui.Confirm(ctx, text) // Ждём согласие, отказ или отмену шага.
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("подтверждение не получено")
		}
		return nil
	}
}

// Play создаёт шаг воспроизведения WAV, который подготавливает encode.
// Буфер затирается после окончания звука; сам encode выполняется в момент шага.
func Play(ui Interaction, encode func() ([]byte, error)) Step {
	return func(ctx context.Context) error {
		wav, err := encode() // Готовим сигнал непосредственно перед воспроизведением.
		if err != nil {
			return err
		}
		defer clear(wav)         // Затираем сигнал после использования или отмены.
		return ui.Play(ctx, wav) // Ждём конца звука, не забирая буфер у UI.
	}
}

// PairWiFi создаёт шаг сопряжения колонки по Wi-Fi: сигнал квазара
// воспроизводится, пока пользователь не подтвердит подключение.
// Пароль передаётся в quasarsound.Config и не сохраняется после шага.
func PairWiFi(ui Interaction, config quasarsound.Config, confirm string) Step {
	return func(ctx context.Context) error {
		defer func() { config.Password = "" }() // Не держим пароль дольше нужного.
		wav, err := quasarsound.Encode(config)  // Собираем сигнал настройки из SSID и пароля.
		if err != nil {
			return err
		}
		defer clear(wav) // Затираем сигнал после использования или отмены.
		for {
			if err := ctx.Err(); err != nil { // Проверяем отмену или истечение срока контекста.
				return err
			}
			if err := ui.Play(ctx, wav); err != nil { // Ждём конца звука перед вопросом.
				return err
			}
			connected, err := ui.Confirm(ctx, confirm) // Проверяем результат вместе с пользователем.
			if err != nil {
				return err
			}
			if connected {
				return nil
			}
		}
	}
}

// Poll повторяет check до первого успеха, пока не истечёт timeout или не отменится контекст.
// Последняя ошибка остаётся видимой вместе с причиной остановки.
func Poll(ctx context.Context, timeout time.Duration, check Step) error {
	ctx, cancel := context.WithTimeout(ctx, timeout) // Ограничиваем ожидание доступным временем.
	defer cancel()
	ticker := time.NewTicker(time.Second) // Повторяем проверку не чаще раза в секунду.
	defer ticker.Stop()
	var last error // Последняя ошибка проверки для итогового сообщения.
	for {
		if err := ctx.Err(); err != nil { // Проверяем отмену или истечение срока контекста.
			return errors.Join(err, last)
		}
		if last = check(ctx); last == nil { // Успешная проверка завершает ожидание.
			return nil
		}
		select {
		case <-ctx.Done(): // Ожидаем отмены или истечения срока.
			return errors.Join(ctx.Err(), last)
		case <-ticker.C:
		}
	}
}

// WaitFor создаёт шаг ожидания готовности: например загрузки станции после перезагрузки.
// Так сценарию не нужен собственный wait_reboot.
func WaitFor(timeout time.Duration, check Step) Step {
	return func(ctx context.Context) error {
		return Poll(ctx, timeout, check) // Повторяем проверку до успеха или таймаута.
	}
}
