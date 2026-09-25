import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { AppDataProvider } from "./components/AppData";
import { ConfirmProvider } from "./components/Confirm";
import { Sidebar } from "./components/Sidebar";
import { ToastProvider } from "./components/Toasts";
import { CatalogPage } from "./pages/CatalogPage";
import { ActivityPage } from "./pages/ActivityPage";
import { SchedulesPage } from "./pages/SchedulesPage";

export function App() {
  return (
    <BrowserRouter>
      <ToastProvider>
        <ConfirmProvider>
          <AppDataProvider>
            <Sidebar />
            <Routes>
              <Route path="/" element={<CatalogPage />} />
              <Route path="/schedules" element={<SchedulesPage />} />
              <Route path="/activity" element={<ActivityPage />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          </AppDataProvider>
        </ConfirmProvider>
      </ToastProvider>
    </BrowserRouter>
  );
}
