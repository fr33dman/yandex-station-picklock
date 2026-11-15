package stages

import (
	"context"
	"errors"

	"github.com/fr33dman/yandex-station-picklock/pkg/boot"
	"github.com/fr33dman/yandex-station-picklock/pkg/scenario"
	"github.com/fr33dman/yandex-station-picklock/pkg/station"
)

func WaitReboot(ctx context.Context) error {
	s, err := session(ctx)
	if err != nil {
		return err
	}
	if s.writtenSHA256 != boot.RootSHA256 {
		return errors.New("verified root boot has not been written")
	}
	err = scenario.Poll(ctx, waitTimeout, func(ctx context.Context) error {
		if err := s.Device.Connect(ctx, s.address); err != nil {
			return err
		}
		if _, err := s.Device.ADB(ctx, "test \"$(getprop sys.boot_completed)\" = 1", false); err != nil {
			return err
		}
		if err := s.Device.Probe(ctx, 2323); err != nil {
			return err
		}
		if _, err := s.Device.Telnet(ctx, rootProof); err != nil {
			return err
		}
		// Verify the actual loaded image, not merely an open port or UID 0.
		output, err := s.Device.Telnet(ctx, bootPreflight+station.Busybox+" sha256sum "+station.BootBlock)
		if err != nil {
			return err
		}
		if !hasDigest(output, boot.RootSHA256) {
			return errors.New("station did not boot the verified root image")
		}
		return nil
	})
	if err == nil {
		s.rootVerified = true
	}
	return err
}
