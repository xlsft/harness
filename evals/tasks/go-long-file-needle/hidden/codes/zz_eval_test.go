package codes

import "testing"

func TestEvalUntouchedEntries(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{293, "sage-maple-0293"},
		{318, "amber-vale-0318"},
		{531, "zephyr-harbor-0531"},
		{532, "yarrow-grove-0532"},
		{641, "sage-ember-0641"},
		{663, "willow-lumen-0663"},
		{700, "yarrow-pine-0700"},
		{745, "lumen-nectar-0745"},
		{860, "zephyr-lumen-0860"},
		{893, "onyx-kelp-0893"},
		{1490, "quartz-quartz-1490"},
		{1530, "onyx-juniper-1530"},
		{1550, "yarrow-onyx-1550"},
		{1562, "yarrow-cedar-1562"},
		{1881, "tundra-basil-1881"},
		{1957, "quartz-cedar-1957"},
		{1981, "ember-delta-1981"},
		{2032, "raven-raven-2032"},
		{2075, "basil-lumen-2075"},
		{2346, "vale-basil-2346"},
		{2392, "delta-fjord-2392"},
		{2393, "tundra-umber-2393"},
		{2579, "umber-raven-2579"},
		{2702, "tundra-willow-2702"},
		{2827, "iris-maple-2827"},
		{2944, "cedar-sage-2944"},
		{3348, "iris-vale-3348"},
		{3424, "basil-ember-3424"},
		{3640, "pine-pine-3640"},
		{3678, "tundra-harbor-3678"},
		{3696, "quartz-umber-3696"},
		{4043, "grove-ember-4043"},
		{4324, "harbor-umber-4324"},
		{4375, "sage-ember-4375"},
		{4424, "grove-basil-4424"},
		{4525, "grove-cedar-4525"},
		{4605, "xenon-sage-4605"},
		{4745, "umber-tundra-4745"},
		{4837, "xenon-fjord-4837"},
		{4940, "basil-amber-4940"},
	}
	for _, c := range cases {
		if got := Describe(c.code); got != c.want {
			t.Errorf("Describe(%d) = %q, want %q", c.code, got, c.want)
		}
	}
	if !Valid(4217) {
		t.Errorf("code 4217 is still invalid: %q", Describe(4217))
	}
}
