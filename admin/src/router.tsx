import { createBrowserRouter, Navigate } from 'react-router-dom'
import { AdminLayout } from './layouts/AdminLayout'
import { DashboardPage } from './pages/DashboardPage'
import { UsersPage } from './pages/UsersPage'
import { FilesPage } from './pages/FilesPage'
import { HashesPage } from './pages/HashesPage'
import { LogsPage } from './pages/LogsPage'
import { LoginPage } from './pages/LoginPage'

export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  {
    path: '/',
    element: <AdminLayout />,
    children: [
      { index: true, element: <Navigate to="/dashboard" replace /> },
      { path: 'dashboard', element: <DashboardPage /> },
      { path: 'users', element: <UsersPage /> },
      { path: 'files', element: <FilesPage /> },
      { path: 'hashes', element: <HashesPage /> },
      { path: 'logs', element: <LogsPage /> },
    ],
  },
])
