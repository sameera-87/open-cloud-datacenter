import {
  Body1,
  Card,
  Spinner,
  Subtitle2,
  Title2,
  makeStyles,
  shorthands,
  tokens,
} from '@fluentui/react-components';
import { Add24Regular, ChevronRight24Regular, Cube24Regular } from '@fluentui/react-icons';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useAuth } from '../auth/useAuth';
import RegisterProjectDialog from '../components/RegisterProjectDialog';

const useStyles = makeStyles({
  root: {
    minHeight: '100vh',
    backgroundColor: tokens.colorNeutralBackground2,
    padding: tokens.spacingHorizontalXXL,
  },
  inner: {
    maxWidth: '720px',
    margin: '0 auto',
    paddingTop: tokens.spacingVerticalXXXL,
  },
  header: {
    marginBottom: tokens.spacingVerticalXL,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
  },
  headerSubtitle: {
    color: tokens.colorNeutralForeground3,
    fontWeight: 400,
  },
  list: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
  },
  projectCard: {
    cursor: 'pointer',
    padding: tokens.spacingHorizontalL,
    transition: 'background-color 0.1s',
    ':hover': { backgroundColor: tokens.colorNeutralBackground1Hover },
  },
  projectRow: {
    display: 'grid',
    gridTemplateColumns: 'auto 1fr auto',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
  },
  iconWrap: {
    width: '36px',
    height: '36px',
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
    display: 'grid',
    placeItems: 'center',
  },
  projectTextStack: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXXS,
    minWidth: 0,
  },
  projectName: { fontWeight: 600 },
  projectMeta: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
  },
  empty: {
    padding: tokens.spacingHorizontalXXL,
    textAlign: 'center',
    color: tokens.colorNeutralForeground3,
  },
  createCard: {
    cursor: 'pointer',
    padding: tokens.spacingHorizontalL,
    ...shorthands.borderStyle('dashed'),
    transition: 'background-color 0.1s',
    ':hover': { backgroundColor: tokens.colorNeutralBackground1Hover },
  },
  createIconWrap: {
    width: '36px',
    height: '36px',
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
    display: 'grid',
    placeItems: 'center',
  },
  createLabel: {
    fontWeight: 600,
    color: tokens.colorBrandForeground1,
  },
});

interface Project {
  id: string;
  name: string;
  description?: string;
  cpu_cores: number;
  memory_gb: number;
  storage_gb: number;
}

export default function ProjectPickerPage() {
  const styles = useStyles();
  const navigate = useNavigate();
  const api = useApi();
  const { tenantId } = useParams<{ tenantId: string }>();
  const { user } = useAuth();
  const isAdmin = user?.isAdmin ?? false;
  const [dialogOpen, setDialogOpen] = useState(false);

  // Determine if user is owner of this tenant to show create button
  const tenantsQuery = useQuery({
    queryKey: ['tenants'],
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data ?? [];
    },
  });
  const myRoles = tenantsQuery.data?.find((t) => t.id === tenantId)?.roles ?? [];
  const isOwner = myRoles.includes('owner');
  const canCreate = isAdmin || isOwner;

  const tenantName = tenantsQuery.data?.find((t) => t.id === tenantId)?.name ?? tenantId;

  const projectsQuery = useQuery({
    queryKey: ['projects', tenantId],
    enabled: Boolean(tenantId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects', {
        params: { path: { tenant_id: tenantId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Project[];
    },
  });

  const projects = projectsQuery.data ?? [];

  return (
    <div className={styles.root}>
      <div className={styles.inner}>
        <div className={styles.header}>
          <Title2>Choose a project</Title2>
          <Subtitle2 className={styles.headerSubtitle}>
            Projects in tenant <strong>{tenantName}</strong>. Switch any time from the top bar.
          </Subtitle2>
        </div>

        {projectsQuery.isLoading && (
          <Card>
            <div className={styles.empty}>
              <Spinner label="Loading projects…" />
            </div>
          </Card>
        )}

        {projectsQuery.isError && (
          <Card>
            <div className={styles.empty}>
              <Body1 style={{ color: tokens.colorPaletteRedForeground1 }}>
                Failed to load projects: {(projectsQuery.error as Error).message}
              </Body1>
            </div>
          </Card>
        )}

        {!projectsQuery.isLoading && !projectsQuery.isError && projects.length === 0 && !canCreate && (
          <Card>
            <div className={styles.empty}>
              <Body1>No projects yet. Ask an owner of {tenantName} to create one.</Body1>
            </div>
          </Card>
        )}

        {!projectsQuery.isLoading && !projectsQuery.isError && (projects.length > 0 || canCreate) && (
          <div className={styles.list}>
            {projects.map((p) => (
              <Card
                key={p.id}
                className={styles.projectCard}
                onClick={() => navigate(`/tenants/${tenantId}/projects/${p.id}/dashboard`)}
              >
                <div className={styles.projectRow}>
                  <div className={styles.iconWrap}>
                    <Cube24Regular />
                  </div>
                  <div className={styles.projectTextStack}>
                    <Body1 className={styles.projectName}>{p.name}</Body1>
                    <Body1 className={styles.projectMeta}>
                      {p.id}
                      {p.description ? ` · ${p.description}` : ''}
                    </Body1>
                    <Body1 className={styles.projectMeta}>
                      {p.cpu_cores} vCPU · {p.memory_gb} GB RAM · {p.storage_gb} GB storage
                    </Body1>
                  </div>
                  <ChevronRight24Regular style={{ color: tokens.colorNeutralForeground3 }} />
                </div>
              </Card>
            ))}

            {canCreate && (
              <Card className={styles.createCard} onClick={() => setDialogOpen(true)}>
                <div className={styles.projectRow}>
                  <div className={styles.createIconWrap}>
                    <Add24Regular />
                  </div>
                  <div className={styles.projectTextStack}>
                    <Body1 className={styles.createLabel}>Create new project</Body1>
                    <Body1 className={styles.projectMeta}>
                      Set a quota and start provisioning resources
                    </Body1>
                  </div>
                  <ChevronRight24Regular style={{ color: tokens.colorNeutralForeground3 }} />
                </div>
              </Card>
            )}
          </div>
        )}
      </div>

      {tenantId && (
        <RegisterProjectDialog
          open={dialogOpen}
          onOpenChange={setDialogOpen}
          tenantId={tenantId}
        />
      )}
    </div>
  );
}
