import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";
import App from "./App";

// 网页界面才有手机布局（phone: 变体）：App 窗口再窄也保持桌面布局
if (!("__TAURI_INTERNALS__" in window)) document.documentElement.dataset.web = "";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
