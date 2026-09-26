package ingest_test

import (
	"testing"

	"github.com/illumination-k/mutrim/ingest"
)

// The symbols are those rustc 1.94 wrote for a small crate, mg, and the
// rsdemo crate of the importer tests.
func TestDemangle(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"_RNvCsjJghpr7hpzZ_6rsdemo5clamp", "rsdemo::clamp"},
		{"_RNvNtCsjJghpr7hpzZ_6rsdemo5testss_3low", "rsdemo::tests::low"},
		{"_RNvNtCsjJghpr7hpzZ_6rsdemo5testss_9mid_again", "rsdemo::tests::mid_again"},
		{"_RNvCsbPI6GZpw7ws_4signs_3neg", "sign::neg"},
		// Closures, ending in the instantiating crate as a backref.
		{"_RNCNvCseiwy8MgjPLM_2mg5apply0B3_", "mg::apply::{closure}"},
		{"_RNCNvNtCseiwy8MgjPLM_2mg5testss_1t0B5_", "mg::tests::t::{closure}"},
		// A generic instance: twice::<i32>.
		{"_RINvCseiwy8MgjPLM_2mg5twicelEB2_", "mg::twice"},
		// An inherent method, its type a backref to the crate.
		{"_RNvMCseiwy8MgjPLM_2mgNtB2_1P3get", "<mg::P>::get"},
		// llvm-cov prefixes a function of internal linkage with its file.
		{"src/lib.rs:_RNvCsjJghpr7hpzZ_6rsdemo5clamp", "rsdemo::clamp"},
		// Legacy mangling, with its hash dropped and its escapes kept.
		{"_ZN2mg5tests1t17he5d4697c29721909E", "mg::tests::t"},
		{"_ZN2mg5apply28_$u7b$$u7b$closure$u7d$$u7d$17hbce34a339eaefe7bE", "mg::apply::_$u7b$$u7b$closure$u7d$$u7d$"},
		// Not Rust, or not read: returned as it is.
		{"main", "main"},
		{"_RNvCs_", "_RNvCs_"},
		{"_ZN3abc", "_ZN3abc"},
	} {
		if got := ingest.Demangle(tc.name); got != tc.want {
			t.Errorf("Demangle(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
