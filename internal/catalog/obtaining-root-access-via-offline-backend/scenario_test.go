package obtaining_root_access_via_offline_backend

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/fr33dman/yandex-station-picklock/artifacts"
	"github.com/fr33dman/yandex-station-picklock/internal/catalog/obtaining-root-access-via-offline-backend/stages"
	"github.com/fr33dman/yandex-station-picklock/pkg/backend/ota"
	"github.com/fr33dman/yandex-station-picklock/pkg/boot"
	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
	"github.com/fr33dman/yandex-station-picklock/pkg/station"
)

type testUI struct {
	playbacks, wifiChecks, downloadChecks int
	retry                                 bool
	declineWrite                          bool
	entered, release                      chan struct{}
}

func (u *testUI) Ask(ctx context.Context, q stages.Question) (string, error) {
	if u.entered != nil {
		close(u.entered)
		u.entered = nil
		select {
		case <-u.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	switch {
	case strings.Contains(q.Text, "Модель"):
		return "station-1", nil
	case strings.Contains(q.Text, "SSID"):
		return "isolated", nil
	case strings.Contains(q.Text, "Пароль"):
		return "password123", nil
	case strings.Contains(q.Text, "IPv4"):
		return "192.168.20.100", nil
	default:
		return "", fmt.Errorf("unexpected question: %s", q.Text)
	}
}

func (u *testUI) Confirm(ctx context.Context, text string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.Contains(text, "скрытая") {
		return false, nil
	}
	if strings.Contains(text, "подключилась") {
		u.wifiChecks++
		if u.retry && u.wifiChecks == 1 {
			return false, nil
		}
	}
	if strings.Contains(text, "обновление скачано") {
		u.downloadChecks++
		if u.retry && u.downloadChecks == 1 {
			return false, nil
		}
	}
	if u.declineWrite && strings.HasPrefix(text, "Записать boot") {
		return false, nil
	}
	return true, nil
}

func (u *testUI) Play(ctx context.Context, wav []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(wav) < 44 || string(wav[:4]) != "RIFF" {
		return errors.New("not a WAV")
	}
	u.playbacks++
	return nil
}

type testBackend struct {
	updates []bool
	closed  int
	waits   int
}

func (b *testBackend) Replace(ctx context.Context, update *ota.Update) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.updates = append(b.updates, update != nil)
	if update != nil {
		manifest, err := update.Check(artifacts.Station1FactoryVersion, "yandexstation")
		if err != nil {
			return err
		}
		if !manifest.HasUpdate || manifest.CRC32 != 2085138043 {
			return errors.New("incorrect embedded OTA manifest")
		}
	}
	return nil
}
func (b *testBackend) WaitDownload(ctx context.Context) error { b.waits++; return ctx.Err() }
func (b *testBackend) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.closed++
	return nil
}

// The virtual device implements files and transport outcomes; it never executes
// shell scripts or connects to a real ADB/Telnet/SSH endpoint.
type testDevice struct {
	files                                         map[string][]byte
	boot                                          []byte
	hostKey                                       string
	booted, telnet, ssh, pinned, firewall, locked bool
	corruptUpload, corruptWrite, rejectSSH        bool
	writes, reboots                               int
	onWrite                                       func(context.Context)
}

var ddPattern = regexp.MustCompile(` dd if=(?:'([^']+)'|([^\s]+)) of=(?:'([^']+)'|([^\s]+)) bs=1048576`)
var hashPattern = regexp.MustCompile(`sha256sum (?:'([^']+)'|([^\s|)]+))`)
var rcSourcePattern = regexp.MustCompile(`(?m)^SRC='([^']+)'$`)
var rcBackupPattern = regexp.MustCompile(`(?m)^BACKUP='([^']+)'$`)
var firewallCopyPattern = regexp.MustCompile(` cp '([^']+)' /data/dropbear/firewall.sh`)

func (d *testDevice) Connect(ctx context.Context, ip netip.Addr) error { return ctx.Err() }
func (d *testDevice) Probe(ctx context.Context, port uint16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if port == 2323 && !d.telnet {
		return errors.New("connection refused")
	}
	return nil
}
func (d *testDevice) ADB(ctx context.Context, script string, root bool) (string, error) {
	return d.run(ctx, script)
}
func (d *testDevice) Push(ctx context.Context, local, remote string) error {
	data, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	if d.corruptUpload && strings.HasSuffix(remote, "/candidate") {
		data[0] ^= 1
	}
	d.files[remote] = data
	return ctx.Err()
}
func (d *testDevice) Pull(ctx context.Context, remote, local string) error {
	data, ok := d.files[remote]
	if !ok {
		return fmt.Errorf("missing remote file: %s", remote)
	}
	return os.WriteFile(local, data, 0600)
}
func (d *testDevice) Reboot(ctx context.Context) error {
	d.reboots++
	d.booted = true
	d.telnet = true
	return ctx.Err()
}
func (d *testDevice) TrustHostKey(name, key string) error {
	if key != d.hostKey {
		return errors.New("host key differs")
	}
	d.pinned = true
	return nil
}
func (d *testDevice) Telnet(ctx context.Context, script string) (string, error) {
	if !d.telnet {
		return "", errors.New("Telnet unavailable")
	}
	if strings.Contains(script, "/data/dropbear.install") {
		d.ssh = true
	}
	if strings.Contains(script, "dropbearkey -y") {
		return "Public key portion is:\n" + d.hostKey + "\n", nil
	}
	return d.run(ctx, script)
}
func (d *testDevice) SSH(ctx context.Context, script string) (string, error) {
	if !d.ssh || !d.pinned || d.rejectSSH {
		return "", errors.New("SSH verification failed")
	}
	if source := rcSourcePattern.FindStringSubmatch(script); source != nil {
		backup := rcBackupPattern.FindStringSubmatch(script)
		if backup == nil {
			return "", errors.New("rc.local was not backed up")
		}
		old := d.files["/system/etc/rc.local"]
		if !strings.Contains(script, station.Quote(boot.SHA256(old))) {
			return "", errors.New("old rc.local hash was not guarded")
		}
		d.files[backup[1]] = bytes.Clone(old)
		d.files["/system/etc/rc.local"] = bytes.Clone(d.files[source[1]])
	}
	if source := firewallCopyPattern.FindStringSubmatch(script); source != nil {
		d.files["/data/dropbear/firewall.sh"] = bytes.Clone(d.files[source[1]])
		d.firewall = true
	}
	if strings.Contains(script, "stop rootshell") {
		d.telnet = false
	}
	return d.run(ctx, script)
}

func (d *testDevice) run(ctx context.Context, script string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.Contains(script, "mkdir /data/local/tmp/picklock_boot_write.lock") {
		if d.locked {
			return "", errors.New("write lock exists")
		}
		d.locked = true
	}
	if strings.Contains(script, "rmdir /data/local/tmp/picklock_boot_write.lock") {
		d.locked = false
	}
	if strings.Contains(script, "sys.boot_completed") && !d.booted {
		return "", errors.New("not booted")
	}
	if command := ddPattern.FindStringSubmatch(script); command != nil {
		source, target := command[1]+command[2], command[3]+command[4]
		if source == station.BootBlock {
			d.files[target] = bytes.Clone(d.boot)
		} else if target == station.BootBlock {
			if !d.locked || !strings.Contains(script, station.Quote(boot.SHA256(d.boot))) {
				return "", errors.New("boot was not guarded by a lock and current digest")
			}
			data, ok := d.files[source]
			if !ok {
				return "", errors.New("candidate missing")
			}
			d.boot = bytes.Clone(data)
			d.writes++
			if d.corruptWrite {
				d.boot[0] ^= 1
			}
			if d.onWrite != nil {
				d.onWrite(ctx)
			}
		}
	}
	if matches := hashPattern.FindAllStringSubmatch(script, -1); len(matches) > 0 {
		last := matches[len(matches)-1]
		name := last[1] + last[2]
		data := d.files[name]
		if name == station.BootBlock {
			data = d.boot
		}
		return boot.SHA256(data) + "  " + name, nil
	}
	return "ROOT_OK", nil
}

func fixture(t *testing.T) (stages.Config, *testUI, *testDevice, *testBackend) {
	t.Helper()
	dir := t.TempDir()
	identity := filepath.Join(dir, "identity")
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wire := make([]byte, 51)
	binary.BigEndian.PutUint32(wire[:4], 11)
	copy(wire[4:15], "ssh-ed25519")
	binary.BigEndian.PutUint32(wire[15:19], 32)
	copy(wire[19:], public)
	key := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire) + " test"
	if err := os.WriteFile(identity+".pub", []byte(key), 0600); err != nil {
		t.Fatal(err)
	}
	stock, err := artifacts.ReadFile(artifacts.Station1StockBoot)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := artifacts.ReadFile("ssh/station-1/rc.local.template")
	if err != nil {
		t.Fatal(err)
	}
	return stages.Config{PublicURL: "http://192.168.1.10", BackupDir: dir, IdentityFile: identity}, &testUI{}, &testDevice{files: map[string][]byte{"/system/etc/rc.local": rc}, boot: stock, hostKey: key}, &testBackend{}
}

func TestCompleteScenarioWithUserRetries(t *testing.T) {
	config, ui, device, backend := fixture(t)
	ui.retry = true
	scenario, err := NewScenario(config, ui, device, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := scenario.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if device.writes != 2 || device.reboots != 1 || device.telnet || !device.ssh || !device.firewall || device.locked {
		t.Fatalf("unexpected final device state: writes=%d reboots=%d telnet=%v ssh=%v firewall=%v lock=%v", device.writes, device.reboots, device.telnet, device.ssh, device.firewall, device.locked)
	}
	if boot.SHA256(device.boot) != boot.NoTelnetSHA256 {
		t.Fatal("Telnet service was not permanently removed from boot")
	}
	if !reflect.DeepEqual(backend.updates, []bool{false, true, false, true, false}) || backend.closed != 1 || backend.waits != 2 || ui.playbacks != 2 {
		t.Fatalf("retry/backend lifetime: %+v, playbacks=%d", backend, ui.playbacks)
	}
	snapshot := scenario.Pipeline.Snapshot()
	if snapshot.Status != execution.Succeeded || len(snapshot.Jobs) != 13 {
		t.Fatalf("scenario status: %+v", snapshot)
	}
	for _, job := range snapshot.Jobs {
		if job.Status != execution.Succeeded {
			t.Fatalf("unfinished job: %+v", job)
		}
	}
	for name, digest := range map[string]string{"boot-original.img": boot.StockSHA256, "boot-before-root.img": boot.StockSHA256, "boot-before-root.img.after": boot.RootSHA256, "boot-before-ssh-only.img": boot.RootSHA256, "boot-before-ssh-only.img.after": boot.NoTelnetSHA256} {
		data, err := os.ReadFile(filepath.Join(scenario.BackupDirectory(), name))
		if err != nil || boot.SHA256(data) != digest {
			t.Fatalf("backup %s: %v", name, err)
		}
	}
}

func TestBootFailuresNeverReachReboot(t *testing.T) {
	for _, kind := range []string{"declined", "upload-corrupt", "readback-corrupt", "cancel-during-write"} {
		t.Run(kind, func(t *testing.T) {
			config, ui, device, backend := fixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch kind {
			case "declined":
				ui.declineWrite = true
			case "upload-corrupt":
				device.corruptUpload = true
			case "readback-corrupt":
				device.corruptWrite = true
			case "cancel-during-write":
				device.onWrite = func(critical context.Context) {
					cancel()
					if critical.Err() != nil {
						t.Error("critical write inherited cancellation")
					}
				}
			}
			scenario, err := NewScenario(config, ui, device, backend)
			if err != nil {
				t.Fatal(err)
			}
			err = scenario.Run(ctx)
			if err == nil || device.reboots != 0 || backend.closed != 1 {
				t.Fatalf("unsafe continuation: err=%v reboots=%d close=%d", err, device.reboots, backend.closed)
			}
			if kind == "readback-corrupt" && (!strings.Contains(err.Error(), "DO NOT REBOOT") || !device.locked) {
				t.Fatalf("unverified write did not keep its lock: %v", err)
			}
			if kind == "cancel-during-write" {
				if !errors.Is(err, context.Canceled) || device.writes != 1 || device.locked {
					t.Fatalf("cancellation skipped verification: %v", err)
				}
				data, readErr := os.ReadFile(filepath.Join(scenario.BackupDirectory(), "boot-before-root.img.after"))
				if readErr != nil || boot.SHA256(data) != boot.RootSHA256 {
					t.Fatal("canceled write lost its verified readback")
				}
			}
			if scenario.Pipeline.Snapshot().Jobs[8].Status != execution.Skipped {
				t.Fatal("downstream reboot was not skipped")
			}
		})
	}
}

func TestSSHFailureKeepsTelnetRecovery(t *testing.T) {
	config, ui, device, backend := fixture(t)
	device.rejectSSH = true
	scenario, err := NewScenario(config, ui, device, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := scenario.Run(t.Context()); err == nil {
		t.Fatal("failed SSH was accepted")
	}
	if !device.telnet || device.writes != 1 || device.firewall {
		t.Fatal("Telnet was removed before verifying SSH")
	}
	if scenario.Pipeline.Snapshot().Jobs[11].Status != execution.Skipped {
		t.Fatal("Telnet removal was not skipped")
	}
}

func TestConcurrentScenarioRunDoesNotCloseActiveBackend(t *testing.T) {
	config, ui, device, backend := fixture(t)
	scenario, err := NewScenario(config, ui, device, backend)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		entered := make(chan struct{})
		ui.entered = entered
		ui.release = make(chan struct{})
		result := make(chan error, 1)
		go func() { result <- scenario.Run(ctx) }()
		<-entered
		if err := scenario.Run(t.Context()); !errors.Is(err, execution.ErrAlreadyStarted) {
			t.Fatalf("concurrent run: %v", err)
		}
		if backend.closed != 0 {
			t.Fatal("rejected run closed the active backend")
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled scenario: %v", err)
		}
		if backend.closed != 1 {
			t.Fatal("canceled scenario did not clean up exactly once")
		}
	})
}
