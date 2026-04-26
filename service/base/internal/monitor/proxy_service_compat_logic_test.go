package monitor

import "testing"

func TestProxyCompatRegistrationServiceRecognition(t *testing.T) {
	cases := []struct {
		name       string
		serviceKey string
		stage      string
		want       bool
	}{
		{name: "legacy accio register", serviceKey: "accio-register", stage: "registration", want: true},
		{name: "register service", serviceKey: "register-service", stage: "registration", want: true},
		{name: "register orchestration flow", serviceKey: "register-orchestration:codex_openai_account_task", stage: "registration", want: true},
		{name: "wrong stage", serviceKey: "register-orchestration:codex_openai_account_task", stage: "oauth", want: false},
		{name: "unrelated service", serviceKey: "other-service", stage: "registration", want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotPrefer := proxyCompatShouldPreferHistoricalSuccessRouting(tc.serviceKey, tc.stage)
			if gotPrefer != tc.want {
				t.Fatalf("proxyCompatShouldPreferHistoricalSuccessRouting(%q, %q) = %v, want %v", tc.serviceKey, tc.stage, gotPrefer, tc.want)
			}
			gotCooldown := proxyCompatRequiresStrictDegradedServiceCooldown(tc.serviceKey, tc.stage)
			if gotCooldown != tc.want {
				t.Fatalf("proxyCompatRequiresStrictDegradedServiceCooldown(%q, %q) = %v, want %v", tc.serviceKey, tc.stage, gotCooldown, tc.want)
			}
		})
	}
}
