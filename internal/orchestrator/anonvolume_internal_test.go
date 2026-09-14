package orchestrator

import (
	"regexp"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

func TestAnonVolumeNameStableAndCollisionResistant(t *testing.T) {
	o := &Orchestrator{Project: &compose.Project{Name: "demo"}}

	// Deterministic: the same service+path yields the same name, so a re-up
	// reuses (rather than orphans) the volume.
	if a, b := o.anonVolumeName("web", "/app/node_modules"), o.anonVolumeName("web", "/app/node_modules"); a != b {
		t.Errorf("anonVolumeName must be deterministic: %q != %q", a, b)
	}
	// Paths that sanitize to the same string stay distinct via the hash suffix.
	if a, b := o.anonVolumeName("web", "/a/b"), o.anonVolumeName("web", "/a.b"); a == b {
		t.Errorf("/a/b and /a.b must not collide, both %q", a)
	}
	// Different services don't collide on the same path.
	if a, b := o.anonVolumeName("web", "/x"), o.anonVolumeName("api", "/x"); a == b {
		t.Errorf("different services must not collide, both %q", a)
	}
	// The name is project-namespaced (so `down -v` scoping and multi-project
	// isolation hold).
	if got := o.anonVolumeName("web", "/data"); !strings.HasPrefix(got, "demo_web_") {
		t.Errorf("anon volume name should be project+service namespaced, got %q", got)
	}
}

// A path of letters, digits, `_`, `-`, `/`, `.` and spaces keeps the name it
// always had (the values are the old formula's, worked out apart from the code),
// so a volume an earlier `up` made is found again; any other character is
// written `_`, first, in the middle or last, so the name is one container 1.4.1
// creates (`^[A-Za-z0-9][A-Za-z0-9_.-]*$`). The hash is still the path's own,
// so `/a+b` and `/a_b` stay two volumes.
func TestAnonVolumeNameIsOneTheRuntimeCreates(t *testing.T) {
	o := &Orchestrator{Project: &compose.Project{Name: "demo"}}
	for target, want := range map[string]string{
		"/app/node_modules": "demo_web_app_node_modules_82e5e9c8",
		"/Up_Case-9/x.y z":  "demo_web_Up_Case-9_x_y_z_810f4b7e",
		"/data":             "demo_web_data_49f653fe",
		"/":                 "demo_web__2a0c975e",
		"/a+b":              "demo_web_a_b_17aa1c30",
		"/données":          "demo_web_donn_es_c8cfb9b3",
		"/+lead":            "demo_web__lead_3d822a11",
		"/trail+":           "demo_web_trail__4f93f533",
		"/a++b":             "demo_web_a__b_a57fdb7b",
	} {
		if got := o.anonVolumeName("web", target); got != want {
			t.Errorf("anonVolumeName(web, %q) = %q, want %q", target, got, want)
		}
	}
	// The service and project parts are not rewritten: a service keyed `web.api`
	// and a project named `my-app` keep the names their volumes already have.
	if got, want := (&Orchestrator{Project: &compose.Project{Name: "my-app"}}).anonVolumeName("web.api", "/data"), "my-app_web.api_data_49f653fe"; got != want {
		t.Errorf("anonVolumeName(web.api, /data) in my-app = %q, want %q", got, want)
	}
	valid := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	// Every ASCII character, control characters included: a tab in a path
	// reaches here from a compose file docker compose loads.
	for c := rune(0); c < 128; c++ {
		for _, target := range []string{"/" + string(c) + "x", "/x" + string(c) + "y", "/x" + string(c)} {
			if got := o.anonVolumeName("web", target); !valid.MatchString(got) {
				t.Errorf("anonVolumeName(web, %q) = %q, not a volume name the runtime creates", target, got)
			}
		}
	}
	for _, target := range []string{"/日本語", "/x😀y", "/é"} {
		if got := o.anonVolumeName("web", target); !valid.MatchString(got) {
			t.Errorf("anonVolumeName(web, %q) = %q, not a volume name the runtime creates", target, got)
		}
	}
	if a, b := o.anonVolumeName("web", "/a+b"), o.anonVolumeName("web", "/a_b"); a == b {
		t.Errorf("/a+b and /a_b must not collide, both %q", a)
	}
}

// A name longer than the runtime creates (255 characters) keeps the start of
// the path and the hash of the whole path; one that fits keeps the name it
// always had. The wants are worked out apart from the code (the old formula,
// then the path part cut so the name is 255 characters). The rows put the name
// just under, at and over the limit, the cut inside a run of non-ASCII
// characters (each written `_`) and right after a `/` written `_`, and a
// project and service that alone leave no room for the path.
func TestAnonVolumeNameFitsTheRuntimeLength(t *testing.T) {
	cases := []struct{ name, project, service, target, want string }{
		{"fits253", "demo", "web", "/ppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp", "demo_web_ppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp_69db5ffa"},
		{"fits254", "demo", "web", "/pppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp", "demo_web_pppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp_2e57663e"},
		{"fits255", "demo", "web", "/ppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp", "demo_web_ppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp_41960cca"},
		{"over256", "demo", "web", "/pppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp", "demo_web_ppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp_f93608ce"},
		{"far", "demo", "web", "/seg000/seg001/seg002/seg003/seg004/seg005/seg006/seg007/seg008/seg009/seg010/seg011/seg012/seg013/seg014/seg015/seg016/seg017/seg018/seg019/seg020/seg021/seg022/seg023/seg024/seg025/seg026/seg027/seg028/seg029/seg030/seg031/seg032/seg033/seg034/seg035/seg036/seg037/seg038/seg039/seg040/seg041/seg042/seg043/seg044/seg045/seg046/seg047/seg048/seg049/seg050/seg051/seg052/seg053/seg054/seg055/seg056/seg057/seg058/seg059/seg060/seg061/seg062/seg063/seg064/seg065/seg066/seg067/seg068/seg069/seg070/seg071/seg072/seg073/seg074/seg075/seg076/seg077/seg078/seg079/seg080/seg081/seg082/seg083/seg084/seg085/seg086/seg087/seg088/seg089/seg090/seg091/seg092/seg093/seg094/seg095/seg096/seg097/seg098/seg099/seg100/seg101/seg102/seg103/seg104/seg105/seg106/seg107/seg108/seg109/seg110/seg111/seg112/seg113/seg114/seg115/seg116/seg117/seg118/seg119", "demo_web_seg000_seg001_seg002_seg003_seg004_seg005_seg006_seg007_seg008_seg009_seg010_seg011_seg012_seg013_seg014_seg015_seg016_seg017_seg018_seg019_seg020_seg021_seg022_seg023_seg024_seg025_seg026_seg027_seg028_seg029_seg030_seg031_seg032_seg033_ba5986c9"},
		{"longprefix", "my-long-project-name-for-volumes", "sssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss", "/qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", "my-long-project-name-for-volumes_sssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss_qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq_caa6d14e"},
		{"cut inside non-ASCII", "demo", "web", "/ppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppéééééééééétail", "demo_web_pppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp______9b4cefe8"},
		{"kept part ends with a written slash", "demo", "web", "/pppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp/qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", "demo_web_pppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppppp__26d99fcb"},
		{"only non-ASCII", "demo", "web", "/éééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééé", "demo_web_______________________________________________________________________________________________________________________________________________________________________________________________________________________________________________2b5a4e0e"},
		{"project and service leave no room", "demo", "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss", "/data", "demo_ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss__49f653fe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := (&Orchestrator{Project: &compose.Project{Name: tc.project}}).anonVolumeName(tc.service, tc.target)
			if got != tc.want {
				t.Errorf("anonVolumeName(%s, %s) in %s =\n  %q (%d)\nwant\n  %q (%d)", tc.service, tc.target, tc.project, got, len(got), tc.want, len(tc.want))
			}
		})
	}
	t.Run("two paths that differ only past the cut", func(t *testing.T) {
		o := &Orchestrator{Project: &compose.Project{Name: "demo"}}
		long := "/" + strings.Repeat("x", 300)
		if a, b := o.anonVolumeName("web", long+"/a"), o.anonVolumeName("web", long+"/b"); a == b || len(a) != 255 || len(b) != 255 {
			t.Errorf("want two different 255-character names, got %q (%d) and %q (%d)", a, len(a), b, len(b))
		}
	})
}
