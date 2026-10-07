package gateway

import (
	"fmt"
	"strings"
)

// LocateFunc names a sink's destination from its configuration ALONE.
//
// It is the constructor's twin for `gateway describe`, and the difference is
// the point: a constructor may resolve credentials or open a store, and
// describe runs in a deploy pipeline with no route to any warehouse. So a
// locate function only builds the SDK writer its constructor would build -- a
// struct, no client -- and asks it for its target. The SDK's Locate() stays
// the one place a target is built; the gateway never spells one itself.
//
// The registry is passed so a routing driver can locate what it routes into
// through the same registry, which is what keeps a binary without a driver
// unable to describe it.
type LocateFunc func(s Sink, sinks *Sinks) (string, error)

// RegisterLocator adds one, refusing a duplicate as Register does.
func (s *Sinks) RegisterLocator(name string, fn LocateFunc) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("a locator needs a sink type name")
	}
	if fn == nil {
		return fmt.Errorf("locator %q is nil", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locators == nil {
		s.locators = map[string]LocateFunc{}
	}
	if _, taken := s.locators[name]; taken {
		return fmt.Errorf("a locator for %q is already registered", name)
	}
	s.locators[name] = fn
	return nil
}

// MustRegisterLocator is RegisterLocator for a main.
func (s *Sinks) MustRegisterLocator(name string, fn LocateFunc) {
	if err := s.RegisterLocator(name, fn); err != nil {
		panic(err)
	}
}

// Locate names the destination of one sink block. `known` is false when this
// binary carries no locator for the sink's type: the manifest then lists the
// sink with no target, as unidentified, rather than leaving it out.
func (s *Sinks) Locate(sk Sink) (target string, known bool, err error) {
	s.mu.RLock()
	fn, ok := s.locators[sk.Type]
	s.mu.RUnlock()
	if !ok {
		return "", false, nil
	}
	target, err = fn(sk, s)
	return target, true, err
}

// Located turns a writer's answer into a locate function's: a writer that
// cannot name its destination says "" -- here that is an error naming what is
// missing, because a manifest with a hole in it would publish nothing and say
// nothing.
func Located(target, what string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("cannot name the destination of %s from its configuration", what)
	}
	return target, nil
}

// Pattern makes a table target the pattern of every table under its parent:
// bigquery://p/d/t becomes bigquery://p/d/*. A routing sink writes tables it
// learns from the events, so what it can publish is where they land.
func Pattern(target string) string {
	i := strings.LastIndex(target, "/")
	if i < 0 {
		return target
	}
	return target[:i+1] + "*"
}
