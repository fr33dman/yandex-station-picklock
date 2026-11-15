package stages

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
)

// WaitAccessPoint Stage
// Точку доступа поднимает сам пользователь: на macOS — Internet Sharing,
// на Linux — hostapd и dnsmasq. Скрипт ждёт подтверждения, что сеть уже
// работает, и только после этого проигрывает сигнал настройки.
func WaitAccessPoint(ctx context.Context) error {
	s, err := session(ctx)
	if err != nil {
		return err
	}
	u, err := url.Parse(s.config.PublicURL)
	if err != nil {
		return err
	}
	gateway, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		return err
	}
	text := "Скрипт ждёт, пока вы поднимете точку доступа «" + s.ssid + "».\n" +
		"macOS: System Settings → General → Sharing → Internet Sharing, источник — Ethernet, раздача — Wi-Fi, имя сети и пароль — как в шаге 1.\n" +
		"Linux: hostapd и dnsmasq (DHCP + DNS) на отдельном адаптере.\n" +
		"Колонка должна получить адрес от этой сети и видеть бэкенд по адресу " + gateway.String() + ".\n" +
		"Ответьте «Да», когда точка доступа уже работает."
	started, err := s.UI.Confirm(ctx, text)
	if err != nil {
		return err
	}
	if !started {
		return errors.New("access point is not running; start it and run the scenario again")
	}
	return nil
}
