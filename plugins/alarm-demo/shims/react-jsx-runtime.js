// 构建期 shim：esbuild --jsx=automatic 生成的 jsx/jsxs 调用从这里取
// 控制台自己的 automatic JSX runtime。
const J = globalThis.__CORDIS_CONSOLE.jsxRuntime;

export default J;
export const { jsx, jsxs, jsxDEV, Fragment } = J;
