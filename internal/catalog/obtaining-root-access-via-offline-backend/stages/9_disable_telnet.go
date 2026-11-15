package stages

import (
	"context"
	"errors"

	"github.com/fr33dman/yandex-station-picklock/artifacts"
	"github.com/fr33dman/yandex-station-picklock/pkg/boot"
	"github.com/fr33dman/yandex-station-picklock/pkg/scenario"
)

// DisableTelnet Stage
// Удаляем Telnet из автозагрузки
func DisableTelnet(ctx context.Context) error {
	s, err := session(ctx)
	if err != nil {
		return err
	}
	if !s.sshVerified {
		return errors.New("key-only SSH must be verified before disabling Telnet")
	}
	if _, err := s.Device.SSH(ctx, rootProof); err != nil {
		return err
	}
	packed, err := artifacts.ReadFile(artifacts.Station1NoTelnetRamdisk)
	if err != nil {
		return err
	}
	image, err := boot.DisableTelnet(s.candidate, packed)
	if err != nil {
		return err
	}
	if _, err := s.save("boot-ssh-only.img", image); err != nil {
		return err
	}
	if err := writeBoot(ctx, s, image, boot.RootSHA256, "ssh-only", s.Device.SSH); err != nil {
		return err
	}
	// Removing the service from the stored ramdisk covers future boots. Stop the
	// currently running instance separately, through SSH so its reply survives.
	if _, err := s.Device.SSH(ctx, "stop rootshell"); err != nil {
		return err
	}
	return scenario.Poll(ctx, waitTimeout, func(ctx context.Context) error {
		if _, err := s.Device.SSH(ctx, rootProof+"\ntest \"$(getprop init.svc.rootshell)\" = stopped"); err != nil {
			return err
		}
		if err := s.Device.Probe(ctx, 2323); err == nil {
			return errors.New("Telnet port is still reachable")
		}
		return nil
	})
}
