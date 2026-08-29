import { lazy, StrictMode, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Route, Routes } from 'react-router-dom'
import './i18n'
import './styles.css'
import { PreferencesProvider } from './app/preferences'
import { AuthGate } from './app/auth'
import { AppShell } from './app/shell'
import { LoadingPage } from './pages/LoadingPage'

const DashboardPage = lazy(() => import('./pages/DashboardPage'))
const ServersPage = lazy(() => import('./pages/ServersPage'))
const ClientsPage = lazy(() => import('./pages/ClientsPage'))
const RoutesPage = lazy(() => import('./pages/RoutesPage'))
const SettingsPage = lazy(() => import('./pages/SettingsPage'))

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <PreferencesProvider>
      <BrowserRouter>
        <AuthGate>
          <Suspense fallback={<LoadingPage />}>
            <Routes>
              <Route element={<AppShell />}>
                <Route index element={<DashboardPage />} />
                <Route path="servers" element={<ServersPage />} />
                <Route path="clients" element={<ClientsPage />} />
                <Route path="routes" element={<RoutesPage />} />
                <Route path="settings" element={<SettingsPage />} />
              </Route>
            </Routes>
          </Suspense>
        </AuthGate>
      </BrowserRouter>
    </PreferencesProvider>
  </StrictMode>,
)
