package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A `models` and an entry of `extra_hosts` that hold nothing are refused by docker compose whatever an earlier file holds
// (#1593; every row measured with `docker compose config -q`, v5.5.1): a later `-f` file that writes `models:` or `extra_hosts:
// {h: }` over a value is refused as one that has no value before it, where a null that another key takes is "not given" over a value
// and read. A value a later file writes over a null is read. known marks the rows left as they were, in other layers: a model a service
// refers to that is not declared, a key of a service's `models` entry that docker compose does not take, and an `extra_hosts` item
// that is neither `host=ip` nor `host:ip` (#1783); a known row says what docker compose does where this reads otherwise, and goes red the day that is fixed (the row is then to be turned).
func TestAModelsOrExtraHostsEntryThatHoldsNothingIsRefusedOverAValue(t *testing.T) {
	for _, tc := range []struct {
		name           string
		files          map[string]string
		order          []string
		refused, known bool
	}{
		{"models: ~", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: ~\n"}, []string{"compose.yaml"}, true, false},
		{"models: []", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: []\n"}, []string{"compose.yaml"}, false, false},
		{"models: {}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: {}\n"}, []string{"compose.yaml"}, false, false},
		{"models: [m]", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: [m]\n"}, []string{"compose.yaml"}, false, true},
		{"models: {m: ~}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: {m: ~}\n"}, []string{"compose.yaml"}, false, true},
		{"models: {m: {}}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: {m: {}}\n"}, []string{"compose.yaml"}, false, true},
		{"models: {m: {endpoint_var: X}}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: {m: {endpoint_var: X}}\n"}, []string{"compose.yaml"}, false, true},
		{"models: {m: {model_var: X}}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: {m: {model_var: X}}\n"}, []string{"compose.yaml"}, false, true},
		{"models: {m: 1}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: {m: 1}\n"}, []string{"compose.yaml"}, true, false},
		{"models: {m: [x]}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: {m: [x]}\n"}, []string{"compose.yaml"}, true, false},
		{"models: [1]", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: [1]\n"}, []string{"compose.yaml"}, true, false},
		{"models: [~]", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: [~]\n"}, []string{"compose.yaml"}, true, false},
		{"models: x", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: x\n"}, []string{"compose.yaml"}, true, false},
		{"models: 1", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: 1\n"}, []string{"compose.yaml"}, true, false},
		{"models: {m: {endpoint_var: 1}}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: {m: {endpoint_var: 1}}\n"}, []string{"compose.yaml"}, true, false},
		{"models: {m: {a: 1}}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: {m: {a: 1}}\n"}, []string{"compose.yaml"}, false, true},
		{"extra_hosts: ~", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: ~\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: {a: ~}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: ~}\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: {a: 1}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: 1}\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: {a: x}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: x}\n"}, []string{"compose.yaml"}, false, false},
		{"extra_hosts: {a: [x]}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: [x]}\n"}, []string{"compose.yaml"}, false, false},
		{"extra_hosts: {a: [~]}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: [~]}\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: {a: [1]}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: [1]}\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: {a: {b: 1}}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: {b: 1}}\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: [x]", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [x]\n"}, []string{"compose.yaml"}, false, true},
		{"extra_hosts: [\"a=b\"]", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"a=b\"]\n"}, []string{"compose.yaml"}, false, false},
		{"extra_hosts: [\"a:b\"]", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"a:b\"]\n"}, []string{"compose.yaml"}, false, false},
		{"extra_hosts: [~]", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [~]\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: [1]", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [1]\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: x", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: x\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: 1", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: 1\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: {\"\": x}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {\"\": x}\n"}, []string{"compose.yaml"}, true, false},
		{"extra_hosts: {a: \"\"}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: \"\"}\n"}, []string{"compose.yaml"}, false, false},
		{"extra_hosts: {a: \"1.2.3.4\"}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: \"1.2.3.4\"}\n"}, []string{"compose.yaml"}, false, false},
		{"extra_hosts: {a: [\"1.2.3.4\"]}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: [\"1.2.3.4\"]}\n"}, []string{"compose.yaml"}, false, false},
		{"extra_hosts: {a: [\"\"]}", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {a: [\"\"]}\n"}, []string{"compose.yaml"}, false, false},
		{"later file: models null over a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n", "b.yaml": "services:\n  a:\n    models:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"later file: extra_hosts map null over a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      h:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"later file: extra_hosts value over a null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts:\n      h:\n", "b.yaml": "services:\n  a:\n    extra_hosts: {h: 1.2.3.4}\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"later file: extra_hosts new null key", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      g:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"models list, a later file: null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"models list, a later file: empty list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models: []\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models list, a later file: empty map", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models: {}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models list, a later file: a list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models: [n]\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models list, a later file: a map", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n      n:\n        model_var: Y\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models list, a later file: an entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n      m:\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models list, a later file: an entry value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n      m:\n        endpoint_var: Z\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, a later file: null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m:\n        endpoint_var: X\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"models map, a later file: empty list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m:\n        endpoint_var: X\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models: []\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, a later file: empty map", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m:\n        endpoint_var: X\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models: {}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, a later file: a list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m:\n        endpoint_var: X\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models: [n]\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, a later file: a map", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m:\n        endpoint_var: X\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n      n:\n        model_var: Y\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, a later file: an entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m:\n        endpoint_var: X\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n      m:\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, a later file: an entry value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m:\n        endpoint_var: X\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n      m:\n        endpoint_var: Z\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, empty, a later file: null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m: {}\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"models map, empty, a later file: empty list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m: {}\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models: []\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, empty, a later file: empty map", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m: {}\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models: {}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, empty, a later file: a list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m: {}\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models: [n]\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, empty, a later file: a map", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m: {}\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n      n:\n        model_var: Y\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, empty, a later file: an entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m: {}\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n      m:\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"models map, empty, a later file: an entry value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models:\n      m: {}\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n      m:\n        endpoint_var: Z\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts map, a later file: the entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      h:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"extra_hosts map, a later file: another entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      g:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"extra_hosts map, a later file: the whole null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts map, a later file: the entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    extra_hosts: {h: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts map, a later file: another entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    extra_hosts: {g: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts map, a later file: a list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    extra_hosts: [\"g=5.6.7.8\"]\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts list, a later file: the entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      h:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"extra_hosts list, a later file: another entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      g:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"extra_hosts list, a later file: the whole null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts list, a later file: the entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts: {h: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts list, a later file: another entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts: {g: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts list, a later file: a list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts: [\"g=5.6.7.8\"]\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts list, colon, a later file: the entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h:1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      h:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"extra_hosts list, colon, a later file: another entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h:1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      g:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"extra_hosts list, colon, a later file: the whole null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h:1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts list, colon, a later file: the entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h:1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts: {h: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts list, colon, a later file: another entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h:1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts: {g: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts list, colon, a later file: a list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: [\"h:1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    extra_hosts: [\"g=5.6.7.8\"]\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts map of a list, a later file: the entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: [1.2.3.4]}\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      h:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"extra_hosts map of a list, a later file: another entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: [1.2.3.4]}\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      g:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"extra_hosts map of a list, a later file: the whole null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: [1.2.3.4]}\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts map of a list, a later file: the entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: [1.2.3.4]}\n", "b.yaml": "services:\n  a:\n    extra_hosts: {h: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts map of a list, a later file: another entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: [1.2.3.4]}\n", "b.yaml": "services:\n  a:\n    extra_hosts: {g: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts map of a list, a later file: a list", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: [1.2.3.4]}\n", "b.yaml": "services:\n  a:\n    extra_hosts: [\"g=5.6.7.8\"]\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"extra_hosts: null in the middle file, a value in the last", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    extra_hosts:\n      h:\n", "c.yaml": "services:\n  a:\n    extra_hosts: {h: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml", "c.yaml"}, true, false},
		{"models: null in the middle file, a value in the last", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n  n:\n    model: ai/y\n", "b.yaml": "services:\n  a:\n    models:\n", "c.yaml": "services:\n  a:\n    models: [n]\n"}, []string{"a.yaml", "b.yaml", "c.yaml"}, true, false},
		{"a model that is declared and referred to", map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n    models: [m]\nmodels:\n  m:\n    model: ai/x\n"}, []string{"compose.yaml"}, false, false},
		{"build.extra_hosts map, a later file: the entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    build:\n      context: .\n      extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    build:\n      extra_hosts:\n        h:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"build.extra_hosts map, a later file: another entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    build:\n      context: .\n      extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    build:\n      extra_hosts:\n        g:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"build.extra_hosts map, a later file: the whole null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    build:\n      context: .\n      extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    build:\n      extra_hosts:\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"build.extra_hosts map, a later file: the entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    build:\n      context: .\n      extra_hosts: {h: 1.2.3.4}\n", "b.yaml": "services:\n  a:\n    build:\n      extra_hosts: {h: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"build.extra_hosts list, a later file: the entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    build:\n      context: .\n      extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    build:\n      extra_hosts:\n        h:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"build.extra_hosts list, a later file: another entry null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    build:\n      context: .\n      extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    build:\n      extra_hosts:\n        g:\n"}, []string{"a.yaml", "b.yaml"}, true, false},
		{"build.extra_hosts list, a later file: the whole null", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    build:\n      context: .\n      extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    build:\n      extra_hosts:\n"}, []string{"a.yaml", "b.yaml"}, false, false},
		{"build.extra_hosts list, a later file: the entry a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    build:\n      context: .\n      extra_hosts: [\"h=1.2.3.4\"]\n", "b.yaml": "services:\n  a:\n    build:\n      extra_hosts: {h: 5.6.7.8}\n"}, []string{"a.yaml", "b.yaml"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			_, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Errorf("refused is %v, want %v (err %v)", err != nil, tc.refused, err)
			}
		})
	}
}
