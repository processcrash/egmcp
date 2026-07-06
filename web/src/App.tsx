import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { LoginPage } from './pages/Login';
import { InstanceListPage } from './pages/InstanceList';
import { InstanceCreatePage } from './pages/InstanceCreate';
import { InstanceDetailPage } from './pages/InstanceDetail';
import { PluginsPage } from './pages/Plugins';
import { AppShell } from './components/AppShell';
import { ProtectedRoute } from './components/ProtectedRoute';

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route
          path="/"
          element={
            <ProtectedRoute>
              <AppShell />
            </ProtectedRoute>
          }
        >
          <Route index element={<Navigate to="/instances" replace />} />
          <Route path="instances" element={<InstanceListPage />} />
          <Route path="instances/new" element={<InstanceCreatePage />} />
          <Route path="instances/:slug" element={<InstanceDetailPage />} />
          <Route path="plugins" element={<PluginsPage />} />
          <Route path="*" element={<Navigate to="/instances" replace />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}
