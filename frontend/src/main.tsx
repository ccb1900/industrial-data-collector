import React from "react";
import { createRoot } from "react-dom/client";
import { ConfigProvider, theme as antdTheme } from "antd";
import App from "./App";

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <React.StrictMode>
      <ConfigProvider
    theme={{
      algorithm: antdTheme.darkAlgorithm,
      token: {
        colorPrimary: "#4d6bfe",
        colorBgBase: "#0b0d12",
        colorBgContainer: "#12151d",
        colorBorder: "#232939",
        borderRadius: 8,
      },
    }}
  >
    <App />
  </ConfigProvider>
    </React.StrictMode>
  );
}
