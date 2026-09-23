import starlight from "@astrojs/starlight";
import { defineConfig } from "astro/config";

export default defineConfig({
  site: "https://illumination-k.github.io",
  base: "/mutrim",
  integrations: [
    starlight({
      title: "mutrim",
      description: "Bazel-native mutation testing for Go that also minimizes test suites.",
      social: [
        { icon: "github", label: "GitHub", href: "https://github.com/illumination-k/mutrim" },
      ],
      editLink: {
        baseUrl: "https://github.com/illumination-k/mutrim/edit/main/docs/",
      },
      sidebar: [
        {
          label: "Guides",
          items: [
            "guides/getting-started",
            "guides/inline-directives",
            "guides/schemata",
            "guides/bazel",
            "guides/cross-package",
            "guides/concurrency",
            "guides/error-paths",
            "guides/thresholds",
            "guides/flaky-tests",
            "guides/diff",
            "guides/reporting",
          ],
        },
        { label: "Reference", items: [{ autogenerate: { directory: "reference" } }] },
      ],
    }),
  ],
});
