import { createBrowserRouter, Navigate } from 'react-router-dom'
import { AppLayout } from './layouts/AppLayout'
import { FilesPage } from './pages/FilesPage'
import { TrashPage } from './pages/TrashPage'
import { SharesPage } from './pages/SharesPage'
import { QuotaPage } from './pages/QuotaPage'
import { AdminPage } from './pages/AdminPage'
import { LoginPage } from './pages/LoginPage'
import { ShareViewPage } from './pages/ShareViewPage'
import { NotFoundPage } from './pages/NotFoundPage'

export const router = createBrowserRouter([
  {
    path: '/login',
    element: <LoginPage />,
  },
  {
    path: '/s/:id',
    element: <ShareViewPage />,
  },
  {
    path: '/',
    element: <AppLayout />,
    children: [
      { index: true, element: <Navigate to="/files" replace /> },
      { path: 'files', element: <FilesPage /> },
      { path: 'trash', element: <TrashPage /> },
      { path: 'shares', element: <SharesPage /> },
      { path: 'quota', element: <QuotaPage /> },
      { path: 'admin', element: <AdminPage /> },
    ],
  },
  { path: '*', element: <NotFoundPage /> },
])
