import { useParams } from 'react-router'
import AppSurfaceView from '../../components/apps/AppSurfaceView'

// The "App management" tab of an installation: the app's own administration page, launched on the admin surface and
// filling the console's content area. The console has its own breadcrumb and nowhere to go back to, so the frame
// carries neither a Back button nor a shell crumb.
export default function AppAdminTab() {
  const installation = useParams().installation ?? ''
  return <AppSurfaceView installation={installation} surface="admin" base={`/manage/apps/installations/${installation}/admin`} crumbTail={false} />
}
