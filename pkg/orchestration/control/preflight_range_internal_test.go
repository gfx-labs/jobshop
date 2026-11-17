package control

import (
	"fmt"
	"testing"

	host "github.com/gfx-labs/acphost/pkg/acphost"
	"github.com/gfx-labs/acphost/pkg/userns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rangeYAML has per_session users that come from a label rule and from an
// isolation profile attached by a label rule, not from the pool's own
// isolation, the case the NixOS module assertion does not see (JOB-226).
const rangeYAML = `projects:
  js: {}
agents:
  fake: {command: /bin/false}
isolation_profiles:
  wide:
    user: {uid: 1000, map_uid: 100000, map_gid: 100000, groups: ["100"], per_session: {range: 8}}
defaults:
  isolation_by_label:
    - labels: ["tier:rule-bad"]
      user: {uid: 1000, map_uid: 100000, map_gid: 100000, groups: ["100"], per_session: {range: 8}}
    - labels: ["tier:rule-ok"]
      user: {uid: 1000, map_uid: 100000, map_gid: 100000, groups: ["100"], per_session: {range: 4}}
    - labels: ["tier:profile-bad"]
      profiles: [wide]
pools:
  rule-bad: {agent: fake, dir: %[1]q, labels: ["tier:rule-bad"]}
  rule-ok: {agent: fake, dir: %[1]q, labels: ["tier:rule-ok"]}
  profile-bad: {agent: fake, dir: %[1]q, labels: ["tier:profile-bad"]}
  plain: {agent: fake, dir: %[1]q}
`

// TestPreflightPerSessionRange (JOB-226): a pool whose per_session ids
// come from a label rule or a profile is checked against the host's subid
// range when the config starts or reloads (preflightFailures, #728), not
// at its first spawn. The fake check applies acphost's span rule to a host
// range of 100000..100003, so only the per_session range reaching the
// spec as MapCount decides the result.
func TestPreflightPerSessionRange(t *testing.T) {
	c, _, _ := scanControllerYAML(t, rangeYAML)
	subids := []userns.Range{{Start: 100000, Count: 4}}
	c.opt.Preflight = func(s host.Spec, _ ...host.PreflightOption) (host.PreflightReport, error) {
		var rep host.PreflightReport
		if u := s.User; u != nil {
			err := userns.CheckSpan("uid", "/etc/subuid", u.MapUID, max(u.MapCount, 1), subids)
			ck := host.PreflightCheck{Name: "user.subuid", OK: err == nil}
			if err != nil {
				ck.Reason = err.Error()
			}
			rep.Checks = append(rep.Checks, ck)
		}
		return rep, nil
	}

	failed := c.preflightFailures(t.Context(), c.config())
	for _, pool := range []string{"rule-bad", "profile-bad"} {
		require.Contains(t, failed, pool)
		assert.Contains(t, failed[pool], "100000..100007", "names the range")
		assert.Contains(t, failed[pool], "/etc/subuid")
	}
	assert.NotContains(t, failed, "rule-ok", fmt.Sprint(failed))
	assert.NotContains(t, failed, "plain")
}
