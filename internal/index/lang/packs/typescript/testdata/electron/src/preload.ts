import { contextBridge, ipcRenderer } from "electron";
contextBridge.exposeInMainWorld("api", { listNotes: () => ipcRenderer.invoke("notes:list") });
