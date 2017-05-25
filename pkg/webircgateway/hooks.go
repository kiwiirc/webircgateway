package webircgateway

var hooksRegistered map[string][]interface{}

func init() {
	hooksRegistered = make(map[string][]interface{})
}

func HookRegister(hookName string, p interface{}) {
	_, exists := hooksRegistered[hookName]
	if !exists {
		hooksRegistered[hookName] = make([]interface{}, 0)
	}

	hooksRegistered[hookName] = append(hooksRegistered[hookName], p)
}

type Hook struct {
	ID   string
	Halt bool
}

func (h *Hook) getCallbacks(eventType string) []interface{} {
	f := make([]interface{}, 0)

	callbacks, exists := hooksRegistered[eventType]
	if exists {
		f = callbacks
	}

	return f
}

type HookIrcConnectionPre struct {
	Hook
	Client         *Client
	UpstreamConfig *ConfigUpstream
}

func (h *HookIrcConnectionPre) Dispatch(eventType string) {
	for _, p := range h.getCallbacks(eventType) {
		if f, ok := p.(func(*HookIrcConnectionPre)); ok {
			f(h)
		}
	}
}
