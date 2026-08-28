package quota

import "testing"

func TestARefusalReadsAsASentence(t *testing.T) {
	cases := []struct {
		what string
		d    dimension
		want string
	}{
		{
			what: "a dimension with no unit",
			d:    dimension{name: "instances", limit: 20, consumed: 20, claim: 1},
			want: "the project is limited to 20 instances and already holds 20, " +
				"so 1 more would not fit",
		},
		{
			what: "a dimension with a unit",
			d:    dimension{name: "memory", unit: "MiB", limit: 4096, consumed: 3800, claim: 512},
			want: "the project is limited to 4096 MiB of memory and already holds 3800 MiB, " +
				"so 512 MiB more would not fit",
		},
		{
			what: "a two word dimension",
			d: dimension{name: "volume capacity", unit: "GiB",
				limit: 100, consumed: 90, claim: 20},
			want: "the project is limited to 100 GiB of volume capacity and already holds 90 GiB, " +
				"so 20 GiB more would not fit",
		},
	}

	for _, c := range cases {
		if got := refusal(c.d); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.what, got, c.want)
		}
	}
}
