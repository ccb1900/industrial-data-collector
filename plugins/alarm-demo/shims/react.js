// 构建期 shim：esbuild --alias:react=./shims/react.js 把插件里的 react 导入
// 重定向到这里，运行时从控制台门面取同一 React 实例——hooks 要求全页面
// 只有一个 React，插件绝不能自带。
const R = globalThis.__CORDIS_CONSOLE.React;

export default R;
export const {
  Children, Component, Fragment, PureComponent, StrictMode, Suspense,
  createContext, createElement, cloneElement, createRef, forwardRef,
  isValidElement, lazy, memo, startTransition, useCallback, useContext,
  useDebugValue, useDeferredValue, useEffect, useId, useImperativeHandle,
  useInsertionEffect, useLayoutEffect, useMemo, useOptimistic, useReducer,
  useRef, useState, useSyncExternalStore, useTransition,
} = R;
