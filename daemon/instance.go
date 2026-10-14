package daemon

import (
	"bytes"
	"context"
	"errors"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing/common/x/list"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/experimental/clashmode"
	"github.com/sagernet/sing-box/experimental/deprecated"
	"github.com/sagernet/sing-box/experimental/locale"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

type Instance struct {
	priorityFailover      *priorityFailoverController
	priorityPauseCallback *list.Element[pause.Callback]
	ctx                   context.Context
	cancel                context.CancelFunc
	instance              *box.Box
	connectionManager     adapter.ConnectionManager
	clashMode             *clashmode.Manager
	trafficManager        *trafficcontrol.Manager
	cacheFile             adapter.CacheFile
	pauseManager          pause.Manager
	urlTestHistoryStorage *urltest.HistoryStorage
	outboundManager       adapter.OutboundManager
	endpointManager       adapter.EndpointManager
	logFactory            log.Factory
}

func (s *StartedService) CheckConfig(ctx context.Context, configContent string) error {
	selectedLocale := locale.FromContext(ctx)
	ctx, _ = locale.ContextWithLocale(s.ctx, selectedLocale.Locale)
	ctx = service.ExtendContext(ctx)
	options, err := parseConfig(ctx, configContent)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: options,
	})
	if err == nil {
		instance.Close()
	}
	return err
}

func (s *StartedService) FormatConfig(ctx context.Context, configContent string) (string, error) {
	selectedLocale := locale.FromContext(ctx)
	ctx, _ = locale.ContextWithLocale(s.ctx, selectedLocale.Locale)
	options, err := parseConfig(ctx, configContent)
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(options)
	if err != nil {
		return "", err
	}
	return buffer.String(), nil
}

type OverrideOptions struct {
	PriorityFailoverJSON string
	AutoRedirect         bool
	IncludePackage       []string
	ExcludePackage       []string
}

func (s *StartedService) newInstance(ctx context.Context, profileContent string, overrideOptions *OverrideOptions) (*Instance, error) {
	selectedLocale := locale.FromContext(ctx)
	ctx, _ = locale.ContextWithLocale(s.ctx, selectedLocale.Locale)
	ctx = service.ExtendContext(ctx)
	service.MustRegister[deprecated.Manager](ctx, new(deprecatedManager))
	ctx, cancel := context.WithCancel(ctx)
	options, err := parseConfig(ctx, profileContent)
	if err != nil {
		cancel()
		return nil, err
	}
	if overrideOptions != nil {
		for _, inbound := range options.Inbounds {
			if tunInboundOptions, isTUN := inbound.Options.(*option.TunInboundOptions); isTUN {
				tunInboundOptions.AutoRedirect = overrideOptions.AutoRedirect
				tunInboundOptions.IncludePackage = append(tunInboundOptions.IncludePackage, overrideOptions.IncludePackage...)
				tunInboundOptions.ExcludePackage = append(tunInboundOptions.ExcludePackage, overrideOptions.ExcludePackage...)
				break
			}
		}
	}
	var policy *PriorityFailoverPolicy
	if overrideOptions != nil {
		policy, err = ParsePriorityFailover(options, overrideOptions.PriorityFailoverJSON)
		if err != nil {
			cancel()
			return nil, err
		}
	}
	if s.oomKillerEnabled {
		if !common.Any(options.Services, func(it option.Service) bool {
			return it.Type == C.TypeOOMKiller
		}) {
			oomOptions := &option.OOMKillerServiceOptions{
				KillerDisabled:      s.oomKillerDisabled,
				MemoryLimitOverride: s.oomMemoryLimit,
			}
			options.Services = append(options.Services, option.Service{
				Type:    C.TypeOOMKiller,
				Options: oomOptions,
			})
		}
	}
	urlTestHistoryStorage := urltest.NewHistoryStorage()
	ctx = service.ContextWithPtr(ctx, urlTestHistoryStorage)
	i := &Instance{
		ctx:                   ctx,
		cancel:                cancel,
		urlTestHistoryStorage: urlTestHistoryStorage,
	}
	var onOutboundsReady func(context.Context) error
	if policy != nil {
		onOutboundsReady = func(ctx context.Context) error { i.priorityFailover.cycle(true); return ctx.Err() }
	}
	boxInstance, err := box.New(box.Options{
		OnOutboundsReady:  onOutboundsReady,
		Context:           ctx,
		Options:           options,
		PlatformLogWriter: s,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	i.instance = boxInstance
	i.connectionManager = service.FromContext[adapter.ConnectionManager](ctx)
	i.clashMode = service.PtrFromContext[clashmode.Manager](ctx)
	i.trafficManager = service.PtrFromContext[trafficcontrol.Manager](ctx)
	i.pauseManager = service.FromContext[pause.Manager](ctx)
	i.cacheFile = service.FromContext[adapter.CacheFile](ctx)
	i.outboundManager = service.FromContext[adapter.OutboundManager](ctx)
	i.endpointManager = service.FromContext[adapter.EndpointManager](ctx)
	i.logFactory = boxInstance.LogFactory()
	if policy != nil {
		outbound, ok := i.outboundManager.Outbound(policy.Selector)
		selector, isSelector := outbound.(*group.Selector)
		if !ok || !isSelector {
			cancel()
			boxInstance.Close()
			return nil, errors.New("priority failover selector missing")
		}
		i.priorityFailover, err = newPriorityFailoverController(ctx, *policy, &priorityInstanceBackend{i, selector})
		if err != nil {
			cancel()
			boxInstance.Close()
			return nil, err
		}
		selector.SetSelectOutboundHandler(func(tag string) bool { return i.priorityFailover.Manual(tag) == nil })
		i.priorityFailover.transition = func(state PriorityFailoverState) {
			i.logFactory.Logger().Info("priority failover: ", state.Mode, " selected=", state.Selected, " primary=", state.PrimaryHealth, " all_down=", state.AllDown)
		}
		i.priorityPauseCallback = i.pauseManager.RegisterCallback(func(event int) { i.priorityFailover.SetPaused(i.pauseManager.IsPaused()) })
		i.priorityFailover.SetPaused(i.pauseManager.IsPaused())
	}
	log.SetStdLogger(boxInstance.LogFactory().Logger())
	return i, nil
}

func attachInstance(ctx context.Context) *Instance {
	return &Instance{
		ctx:                   ctx,
		connectionManager:     service.FromContext[adapter.ConnectionManager](ctx),
		clashMode:             service.PtrFromContext[clashmode.Manager](ctx),
		trafficManager:        service.PtrFromContext[trafficcontrol.Manager](ctx),
		pauseManager:          service.FromContext[pause.Manager](ctx),
		cacheFile:             service.FromContext[adapter.CacheFile](ctx),
		urlTestHistoryStorage: service.PtrFromContext[urltest.HistoryStorage](ctx),
		outboundManager:       service.FromContext[adapter.OutboundManager](ctx),
		endpointManager:       service.FromContext[adapter.EndpointManager](ctx),
		logFactory:            service.FromContext[log.Factory](ctx),
	}
}

func (i *Instance) Start() error {
	if err := i.instance.Start(); err != nil {
		return err
	}
	if i.priorityFailover != nil {
		i.priorityFailover.Start()
	}
	return nil
}

func (i *Instance) Close() error {
	i.cancel()
	if i.priorityFailover != nil {
		i.pauseManager.UnregisterCallback(i.priorityPauseCallback)
		i.priorityFailover.Close()
	}
	i.urlTestHistoryStorage.Close()
	return i.instance.Close()
}

func (i *Instance) Box() *box.Box {
	return i.instance
}

func (i *Instance) PauseManager() pause.Manager {
	return i.pauseManager
}

func (i *Instance) TrafficManager() *trafficcontrol.Manager {
	return i.trafficManager
}

func parseConfig(ctx context.Context, configContent string) (option.Options, error) {
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(configContent))
	if err != nil {
		return option.Options{}, E.Cause(err, "decode config")
	}
	return options, nil
}
