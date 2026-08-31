package pool

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const (
	// Type is the outbound type name exposed to sing-box.
	Type = "pool"
	// Tag is the default outbound tag used by builder.
	Tag = "proxy-pool"
	// BootstrapTag is the pool used to reach source groups that cannot dial directly.
	BootstrapTag = "proxy-bootstrap"

	modeAuto       = "auto"
	modeSequential = "sequential"
	modeRandom     = "random"
	modeBalance    = "balance"
)

// Options controls pool outbound behaviour.
type Options struct {
	Mode              string
	Members           []string
	FailureThreshold  int
	BlacklistDuration time.Duration
	Metadata          map[string]MemberMeta
	MaxRetries        int           // max retry attempts on connection failure (default 2)
	SessionTTL        time.Duration // idle TTL for session-strategy stickiness (default 10m)
}

// MemberMeta carries optional descriptive information for monitoring UI.
type MemberMeta struct {
	Name           string
	URI            string
	Mode           string
	ListenAddress  string
	Port           uint16
	Region         string // GeoIP region code: "jp", "kr", "us", "hk", "tw", "other"
	Country        string // Full country name from GeoIP
	CountryISO     string // ISO country code from GeoIP (e.g. "US", "JP")
	SourceKind     string
	SourceName     string
	SourceRef      string
	ProtocolFamily string
	NodeMode       string
	DomainFamily   string
}

// Register wires the pool outbound into the registry.
func Register(registry *outbound.Registry) {
	outbound.Register[Options](registry, Type, newPool)
}

type memberState struct {
	outbound adapter.Outbound
	tag      string
	entry    *monitor.EntryHandle
	shared   *sharedMemberState
}

type poolOutbound struct {
	outbound.Adapter
	ctx            context.Context
	logger         log.ContextLogger
	manager        adapter.OutboundManager
	options        Options
	mode           string
	members        []*memberState
	mu             sync.Mutex
	rrCounter      atomic.Uint32
	rng            *rand.Rand
	rngMu          sync.Mutex // protects rng for random mode
	monitor        *monitor.Manager
	candidatesPool sync.Pool
	sticky         *stickyState
}

func newPool(ctx context.Context, _ adapter.Router, logger log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	if len(options.Members) == 0 {
		return nil, E.New("pool requires at least one member")
	}
	manager := service.FromContext[adapter.OutboundManager](ctx)
	if manager == nil {
		return nil, E.New("missing outbound manager in context")
	}
	monitorMgr := monitor.FromContext(ctx)
	normalized := normalizeOptions(options)
	memberCount := len(normalized.Members)
	p := &poolOutbound{
		Adapter: outbound.NewAdapter(Type, tag, []string{N.NetworkTCP, N.NetworkUDP}, normalized.Members),
		ctx:     ctx,
		logger:  logger,
		manager: manager,
		options: normalized,
		mode:    normalized.Mode,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
		monitor: monitorMgr,
		candidatesPool: sync.Pool{
			New: func() any {
				return make([]*memberState, 0, memberCount)
			},
		},
		sticky: newStickyState(normalized.SessionTTL),
	}

	// Register nodes immediately if monitor is available
	if monitorMgr != nil {
		logger.Info("registering ", len(normalized.Members), " nodes to monitor")
		for _, memberTag := range normalized.Members {
			// Acquire shared state for this tag (creates if not exists)
			state := acquireSharedState(memberTag)

			meta := normalized.Metadata[memberTag]
			info := monitor.NodeInfo{
				Tag:            memberTag,
				Name:           meta.Name,
				URI:            meta.URI,
				Mode:           meta.Mode,
				ListenAddress:  meta.ListenAddress,
				Port:           meta.Port,
				Region:         meta.Region,
				Country:        meta.Country,
				SourceKind:     meta.SourceKind,
				SourceName:     meta.SourceName,
				SourceRef:      meta.SourceRef,
				ProtocolFamily: meta.ProtocolFamily,
				NodeMode:       meta.NodeMode,
				DomainFamily:   meta.DomainFamily,
			}
			entry := monitorMgr.Register(info)
			if entry != nil {
				// Attach entry to shared state so all pool instances share it
				state.attachEntry(entry)
				logger.Info("registered node: ", memberTag)
				// Set probe and release functions immediately
				entry.SetRelease(releaseSharedState(state))
				if probeFn := p.makeProbeByTagFunc(memberTag); probeFn != nil {
					entry.SetProbe(probeFn)
				}
			} else {
				logger.Warn("failed to register node: ", memberTag)
			}
		}
	} else {
		logger.Warn("monitor manager is nil, skipping node registration")
	}

	return p, nil
}

func normalizeOptions(options Options) Options {
	if options.FailureThreshold <= 0 {
		options.FailureThreshold = 3
	}
	if options.BlacklistDuration <= 0 {
		options.BlacklistDuration = 24 * time.Hour
	}
	if options.Metadata == nil {
		options.Metadata = make(map[string]MemberMeta)
	}
	if options.MaxRetries < 0 {
		options.MaxRetries = 0
	} else if options.MaxRetries == 0 {
		options.MaxRetries = 2 // default: up to 3 total attempts
	}
	switch strings.ToLower(options.Mode) {
	case modeAuto:
		options.Mode = modeAuto
	case modeRandom:
		options.Mode = modeRandom
	case modeBalance:
		options.Mode = modeBalance
	default:
		options.Mode = modeAuto
	}
	return options
}

func (p *poolOutbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	p.mu.Lock()
	err := p.initializeMembersLocked()
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return nil
}

// initializeMembersLocked must be called with p.mu held
func (p *poolOutbound) initializeMembersLocked() error {
	if len(p.members) > 0 {
		return nil // Already initialized
	}

	members := make([]*memberState, 0, len(p.options.Members))
	for _, tag := range p.options.Members {
		detour, loaded := p.manager.Outbound(tag)
		if !loaded {
			return E.New("pool member not found: ", tag)
		}

		// Acquire shared state (creates if not exists, reuses if already created)
		state := acquireSharedState(tag)

		member := &memberState{
			outbound: detour,
			tag:      tag,
			shared:   state,
			entry:    state.entryHandle(),
		}

		// The constructor registers and binds the monitor entry before sing-box
		// starts the outbound. Reuse that entry during lazy initialization so the
		// first probe does not replace its callback/revision while it is running.
		if member.entry != nil {
			member.entry.SetRelease(p.makeReleaseFunc(member))
			members = append(members, member)
			continue
		}

		// Register a monitor entry when this pool belongs to a fresh reload
		// generation and no constructor-time entry is available.
		if p.monitor != nil {
			meta := p.options.Metadata[tag]
			info := monitor.NodeInfo{
				Tag:            tag,
				Name:           meta.Name,
				URI:            meta.URI,
				Mode:           meta.Mode,
				ListenAddress:  meta.ListenAddress,
				Port:           meta.Port,
				Region:         meta.Region,
				Country:        meta.Country,
				SourceKind:     meta.SourceKind,
				SourceName:     meta.SourceName,
				SourceRef:      meta.SourceRef,
				ProtocolFamily: meta.ProtocolFamily,
				NodeMode:       meta.NodeMode,
				DomainFamily:   meta.DomainFamily,
			}
			entry := p.monitor.Register(info)
			if entry != nil {
				state.attachEntry(entry)
				member.entry = entry
				entry.SetRelease(p.makeReleaseFunc(member))
				if probe := p.makeProbeFunc(member); probe != nil {
					entry.SetProbe(probe)
				}
			}
		}
		members = append(members, member)
	}
	p.members = members
	p.logger.Info("pool initialized with ", len(members), " members")

	return nil
}

func (p *poolOutbound) memberName(member *memberState) string {
	if meta, ok := p.options.Metadata[member.tag]; ok && meta.Name != "" {
		return meta.Name
	}
	return member.tag
}

// StickySnapshot exposes the current stable-bucket and session affinity state
// for observability. Returns an empty snapshot when stickiness is not in use.
func (p *poolOutbound) StickySnapshot() StickySnapshot {
	if p == nil || p.sticky == nil {
		return StickySnapshot{Buckets: map[string]string{}, Sessions: map[string]string{}}
	}
	return p.sticky.snapshot()
}
