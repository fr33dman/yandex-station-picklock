package stages

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/fr33dman/yandex-station-picklock/pkg/backend/ota"
	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
	"github.com/fr33dman/yandex-station-picklock/pkg/station"
)

type Question = execution.Question

// Interaction is the UI boundary. Play receives a WAV containing Wi-Fi credentials;
// Play waits for playback to finish and does not retain the buffer. Adapters
// must not log it or publish it as a downloadable public artifact.
type Interaction = execution.Interaction

// Device contains transport operations only. Hash checks and ordering live in stages.
type Device interface {
	Connect(context.Context, netip.Addr) error
	ADB(context.Context, string, bool) (string, error)
	Push(context.Context, string, string) error
	Pull(context.Context, string, string) error
	Reboot(context.Context) error
	Probe(context.Context, uint16) error
	Telnet(context.Context, string) (string, error)
	SSH(context.Context, string) (string, error)
	TrustHostKey(string, string) error
}

type Backend interface {
	Replace(context.Context, *ota.Update) error
	WaitDownload(context.Context) error
	Close(context.Context) error
}

type Config struct {
	// PublicURL is the local HTTP ingress URL, e.g. http://192.168.1.10.
	PublicURL string
	// BackupDir must survive the scenario; device backups are never deleted here.
	BackupDir string
	// IdentityFile is the private key path. Its public sibling must end in .pub.
	IdentityFile string
}

// Session belongs to one sequential pipeline. It is carried by context rather
// than globals; the pipeline's job completion channels synchronize stage handoff.
type Session struct {
	UI            Interaction
	Device        Device
	Backend       Backend
	config        Config
	directory     string
	model         string
	ssid          string
	password      string
	hidden        bool
	address       netip.Addr
	original      []byte
	candidate     []byte
	writtenSHA256 string
	rootVerified  bool
	sshVerified   bool
	publicKey     string
}

type sessionKey struct{}

func NewSession(config Config, ui Interaction, device Device, backend Backend) (*Session, error) {
	if ui == nil || device == nil || backend == nil {
		return nil, errors.New("UI, device and backend adapters are required")
	}
	u, err := url.Parse(config.PublicURL)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.Is4() || !ip.IsPrivate() || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("public backend URL must be an HTTP URL at a private IPv4 address")
	}
	if config.BackupDir == "" || config.IdentityFile == "" {
		return nil, errors.New("persistent backup directory and SSH identity file are required")
	}
	key, err := os.ReadFile(config.IdentityFile + ".pub")
	if err != nil {
		return nil, fmt.Errorf("read SSH public key: %w", err)
	}
	if err := station.ValidatePublicKey(string(key)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(config.BackupDir, 0700); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(config.BackupDir, "station-1-")
	if err != nil {
		return nil, err
	}
	if err := syncDirectory(config.BackupDir); err != nil {
		return nil, err
	}
	return &Session{UI: ui, Device: device, Backend: backend, config: config, directory: directory, publicKey: string(key)}, nil
}

func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func session(ctx context.Context) (*Session, error) {
	s, ok := ctx.Value(sessionKey{}).(*Session)
	if !ok || s == nil {
		return nil, errors.New("offline-backend session is missing from context")
	}
	return s, nil
}

func (s *Session) BackupDirectory() string { return s.directory }

// save never replaces an existing backup, follows no final symlink and flushes
// the file before allowing a later destructive operation to start.
func (s *Session) save(name string, data []byte) (string, error) {
	filename := filepath.Join(s.directory, name)
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return "", fmt.Errorf("save %s: %w", name, err)
	}
	if err := syncDirectory(s.directory); err != nil {
		return "", err
	}
	return filename, nil
}

func syncDirectory(name string) error {
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// waitTimeout ограничивает ожидание готовности устройства после операции.
// Само ожидание выполняет scenario.Poll из SDK, чтобы у сценария не было
// собственного цикла опроса.
const waitTimeout = 5 * time.Minute
