import { useParams } from 'react-router-dom';

export function useActiveProject() {
  const { tenantId, projectId } = useParams<{ tenantId: string; projectId?: string }>();
  return { tenantId, projectId };
}
