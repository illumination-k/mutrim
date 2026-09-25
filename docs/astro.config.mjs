import starlight from "@astrojs/starlight";
import { defineConfig } from "astro/config";

export default defineConfig({
  site: "https://illumination-k.github.io",
  base: "/mutrim",
  integrations: [
    starlight({
      title: "mutrim",
      defaultLocale: "root",
      locales: {
        root: { label: "English", lang: "en" },
        ja: { label: "日本語", lang: "ja" },
      },
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
          translations: { ja: "ガイド" },
          items: [
            "guides/test",
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
            "guides/sampling",
            "guides/reporting",
          ],
        },
        {
          label: "Reference",
          translations: { ja: "リファレンス" },
          items: [{ autogenerate: { directory: "reference" } }],
        },
      ],
    }),
  ],
});
