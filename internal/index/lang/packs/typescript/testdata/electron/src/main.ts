import { app, BrowserWindow, ipcMain } from "electron";
import { readNotes } from "./notes";

ipcMain.handle("notes:list", async () => readNotes());
ipcMain.on("window:minimize", (event) => BrowserWindow.fromWebContents(event.sender)?.minimize());
// ipcMain.handle("commented-out", () => 1);
const doc = "ipcMain.handle('in-a-string', h)";

app.whenReady().then(() => new BrowserWindow());
