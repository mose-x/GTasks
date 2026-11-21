// code-hooks 的 nodejs:lint 阶段跑 `npx eslint <改动文件>`，改动文件里可能混着
// .go / .sh / .md，所以先把非 JS 文件整体 ignore，避免 eslint 因为「没有匹配的
// 配置」直接报错退出。
//
// 规则集目前是空的：前端是没有构建步骤的原生浏览器 JS（go:embed 直接打进二进制），
// 要打开 eslint:recommended 得先把 window / document / webkit 这些全局声明补齐，
// 否则满屏 no-undef。先让 lint 阶段可跑通，规则后续按需加。
export default [
  {
    ignores: [
      "node_modules/**",
      "build/bin/**",
      "**/*.go",
      "**/*.sh",
      "**/*.py",
      "**/*.toml",
      "**/*.md",
      "**/*.json",
      "**/*.yml",
      "**/*.yaml",
      "**/*.css",
      "**/*.html",
    ],
  },
  {
    files: ["**/*.js", "**/*.mjs"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "module",
    },
    rules: {},
  },
];
