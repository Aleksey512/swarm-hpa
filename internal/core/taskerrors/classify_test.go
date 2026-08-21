package taskerrors

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want Class
	}{
		{
			// The real error from the Swarm cluster this daemon manages
			// (ticket fixture): full vxlan text wrapped in the sandbox-join
			// context. Must classify as the most specific class.
			name: "real swarm vxlan error",
			err:  `network sandbox join failed: subnet sandbox join failed for "10.0.18.0/24": error creating vxlan interface: file exists`,
			want: ClassVxlanFileExists,
		},
		{
			name: "vxlan substring alone",
			err:  `error creating vxlan interface: file exists`,
			want: ClassVxlanFileExists,
		},
		{
			name: "vxlan with extra prefix context",
			err:  `container failed to start: error creating vxlan interface: file exists`,
			want: ClassVxlanFileExists,
		},
		{
			name: "sandbox join without vxlan",
			err:  `network sandbox join failed: subnet sandbox join failed for "10.0.20.0/24": some other reason`,
			want: ClassNetworkSandboxJoin,
		},
		{
			name: "sandbox join substring alone",
			err:  "network sandbox join failed",
			want: ClassNetworkSandboxJoin,
		},
		{
			name: "no such container",
			err:  "No such container: admin_admin_analytics.1.yvnzap3vbegg8msxtv9w7hhtk",
			want: ClassOther,
		},
		{
			name: "non-zero exit",
			err:  "task: non-zero exit (137)",
			want: ClassOther,
		},
		{
			name: "empty string",
			err:  "",
			want: ClassOther,
		},
		{
			// Classification is case-sensitive today: Docker error text is
			// machine-generated with stable casing. Pin the behaviour so a
			// change is deliberate.
			name: "case sensitivity — capitalised vxlan text is other",
			err:  "Error Creating VxLAN Interface: File Exists",
			want: ClassOther,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%q) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}
