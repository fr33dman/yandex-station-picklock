package stages

import (
	"context"
	"errors"
	"net/netip"

	"github.com/fr33dman/yandex-station-picklock/pkg/scenario"
)

func WaitUpdate(ctx context.Context) error {
	s, err := session(ctx)
	if err != nil {
		return err
	}
	for {
		finished, err := s.UI.Confirm(ctx, "Колонка закончила установку обновления и загрузилась?")
		if err != nil {
			return err
		}
		if finished {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	address, err := s.UI.Ask(ctx, Question{Text: "Текущий IPv4-адрес колонки"})
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return errors.New("station address must be a private IPv4 address")
	}
	if err := scenario.Poll(ctx, waitTimeout, func(ctx context.Context) error { return s.Device.Connect(ctx, ip) }); err != nil {
		return err
	}
	s.address = ip
	return nil
}
