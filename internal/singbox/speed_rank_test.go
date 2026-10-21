package singbox

import "testing"

func TestChooseSpeedNode(t *testing.T) {
	tests := []struct {
		name    string
		results []nodeSpeed
		current string
		wantTag string
		wantOK  bool
	}{
		{
			name: "скорость приоритетнее задержки",
			results: []nodeSpeed{
				{tag: "fast", mbps: 100, delay: 200},
				{tag: "low-latency", mbps: 50, delay: 10},
			},
			current: "low-latency",
			wantTag: "fast",
			wantOK:  true,
		},
		{
			name: "гистерезис сохраняет текущий узел",
			results: []nodeSpeed{
				{tag: "best", mbps: 100, delay: 50},
				{tag: "current", mbps: 85, delay: 60},
			},
			current: "current",
			wantTag: "current",
			wantOK:  true,
		},
		{
			name: "переключение при существенной выгоде",
			results: []nodeSpeed{
				{tag: "best", mbps: 100, delay: 50},
				{tag: "current", mbps: 50, delay: 60},
			},
			current: "current",
			wantTag: "best",
			wantOK:  true,
		},
		{
			name:    "пустой список",
			results: nil,
			current: "current",
			wantTag: "",
			wantOK:  false,
		},
		{
			name: "все ошибки",
			results: []nodeSpeed{
				{tag: "a", mbps: 0, delay: 10},
				{tag: "b", mbps: -1, delay: 20},
			},
			current: "a",
			wantTag: "",
			wantOK:  false,
		},
		{
			name: "tie по скорости и задержке — по tag",
			results: []nodeSpeed{
				{tag: "b", mbps: 100, delay: 50},
				{tag: "a", mbps: 100, delay: 50},
			},
			current: "",
			wantTag: "a",
			wantOK:  true,
		},
		{
			name: "tie по скорости — меньше задержка",
			results: []nodeSpeed{
				{tag: "slow", mbps: 100, delay: 80},
				{tag: "fast", mbps: 100, delay: 20},
			},
			current: "",
			wantTag: "fast",
			wantOK:  true,
		},
		{
			name: "текущий отсутствует — берём лучший",
			results: []nodeSpeed{
				{tag: "best", mbps: 100, delay: 50},
				{tag: "other", mbps: 90, delay: 40},
			},
			current: "missing",
			wantTag: "best",
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := chooseSpeedNode(tt.results, tt.current)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got.tag != tt.wantTag {
				t.Fatalf("tag = %q, want %q", got.tag, tt.wantTag)
			}
		})
	}
}
