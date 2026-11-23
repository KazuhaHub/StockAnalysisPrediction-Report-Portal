import { useNavigate, useParams } from 'react-router'
import AppSurfaceView from '../components/apps/AppSurfaceView'

// The administration page of an app for someone who is not a Portal administrator: a user the app was delegated to.
// The same sandboxed frontend as the reader's page, launched on the admin surface, inside the normal app shell.
export default function AppManageV2() {
  const { installation = '' } = useParams()
  const navigate = useNavigate()
  return <AppSurfaceView installation={installation} surface="admin" base={`/apps/manage/${installation}`}
    onBack={() => { void navigate('/apps') }} />
}
