package app

import (
	"log/slog"
	"sync"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
)

// Config bounds the background work of the till.
type Config struct {
	// StockSyncInterval is how often the till pushes its quantities to
	// inventory-service.
	StockSyncInterval time.Duration
	// PollInterval is how often the till asks for new orders. The
	// specification fixes five seconds for the merchant queue (S-M6).
	PollInterval time.Duration
	// KitchenStep is how long one preparation stage takes in the simulation.
	KitchenStep time.Duration
	// OrderPageLimit caps one poll of the queue.
	OrderPageLimit int
	// AutoPublish разрешает кассе публиковать своё меню самой: при старте и
	// после любого сбоя, пока публикация не пройдёт. Выключенная касса ждёт
	// явного POST /pos/publish.
	AutoPublish bool
}

// Service implements the use cases of partner-pos.
//
// The till holds its menu and its behaviour in memory on purpose: it has no
// database (TZ v3.0 §3.3), and everything durable lives in the platform. A
// restart republishes the menu and picks the orders up again from the queue.
type Service struct {
	mu        sync.RWMutex
	menu      domain.Menu
	behavior  domain.Behavior
	published bool
	version   int64
	// pendingSync означает, что касса изменила у себя остатки или стоп-лист и
	// ещё не донесла это до платформы. Фоновый цикл выталкивает только такие
	// изменения: слепая перезапись раз в минуту откатывала бы всё, что
	// заведение или оператор сделали через API платформы.
	pendingSync bool

	platform Platform
	logger   *slog.Logger
	cfg      Config
}

// NewService wires the use cases to the platform port.
func NewService(platform Platform, menu domain.Menu, logger *slog.Logger, cfg Config) *Service {
	if cfg.StockSyncInterval <= 0 {
		cfg.StockSyncInterval = time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.KitchenStep <= 0 {
		cfg.KitchenStep = 20 * time.Second
	}
	if cfg.OrderPageLimit <= 0 {
		cfg.OrderPageLimit = 50
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		menu: menu.Clone(), behavior: domain.DefaultBehavior(),
		platform: platform, logger: logger, cfg: cfg,
	}
}

// MenuState is the till menu together with what the platform knows about it.
type MenuState struct {
	Menu        domain.Menu
	Published   bool
	MenuVersion int64
}

// Menu returns a copy of the till menu.
func (s *Service) Menu() MenuState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return MenuState{Menu: s.menu.Clone(), Published: s.published, MenuVersion: s.version}
}

// Behavior returns the current behaviour of the till.
func (s *Service) Behavior() domain.Behavior {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.behavior
}

// SetBehavior replaces the behaviour of the till.
func (s *Service) SetBehavior(b domain.Behavior) (domain.Behavior, error) {
	if err := b.Validate(); err != nil {
		return domain.Behavior{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.behavior = b
	return s.behavior, nil
}

// PatchItem changes one position of the till menu. It changes nothing in the
// platform: the change reaches it with the next publication and the next stock
// synchronisation, which is exactly how a real till behaves.
func (s *Service) PatchItem(externalID string, patch domain.ItemPatch) (domain.Item, error) {
	if err := patch.Validate(); err != nil {
		return domain.Item{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.menu.Apply(externalID, patch)
	if err != nil {
		return domain.Item{}, err
	}
	if err := s.menu.Validate(); err != nil {
		return domain.Item{}, err
	}
	s.pendingSync = true
	return item, nil
}

// pendingChanges reports whether the till has changes the platform has not
// been told about yet.
func (s *Service) pendingChanges() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pendingSync
}
