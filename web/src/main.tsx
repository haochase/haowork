import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app";

const root = document.getElementById("root");
if (!root) throw new Error("Workbench root element is missing");

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
