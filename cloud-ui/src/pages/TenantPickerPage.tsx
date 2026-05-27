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
import { Add24Regular, Building24Regular, ChevronRight24Regular } from '@fluentui/react-icons';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useAuth } from '../auth/useAuth';
import RegisterTenantDialog from '../components/RegisterTenantDialog';

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
  tenantCard: {
    cursor: 'pointer',
    padding: tokens.spacingHorizontalL,
    transition: 'background-color 0.1s',
    ':hover': { backgroundColor: tokens.colorNeutralBackground1Hover },
  },
  tenantRow: {
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
  tenantTextStack: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXXS,
    minWidth: 0,
  },
  tenantName: { fontWeight: 600 },
  rolesRow: {
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

export default function TenantPickerPage() {
  const styles = useStyles();
  const navigate = useNavigate();
  const api = useApi();
  const { user } = useAuth();
  const isAuthenticated = Boolean(user);
  const isAdmin = user?.isAdmin ?? false;
  const [dialogOpen, setDialogOpen] = useState(false);

  const tenantsQuery = useQuery({
    queryKey: ['tenants'],
    enabled: isAuthenticated,
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data ?? [];
    },
  });

  return (
    <div className={styles.root}>
      <div className={styles.inner}>
        <div className={styles.header}>
          <Title2>Select a tenant</Title2>
          <Subtitle2 className={styles.headerSubtitle}>
            Choose the tenant you want to work in. Switch any time from the top bar.
          </Subtitle2>
        </div>

        {!isAuthenticated && (
          <Card>
            <div className={styles.empty}>
              <Body1>Not signed in. Go to /login to sign in first.</Body1>
            </div>
          </Card>
        )}

        {isAuthenticated && tenantsQuery.isLoading && (
          <Card>
            <div className={styles.empty}>
              <Spinner label="Loading tenants…" />
            </div>
          </Card>
        )}

        {isAuthenticated && tenantsQuery.isError && (
          <Card>
            <div className={styles.empty}>
              <Body1 style={{ color: tokens.colorPaletteRedForeground1 }}>
                Failed to load tenants: {(tenantsQuery.error as Error).message}
              </Body1>
            </div>
          </Card>
        )}

        {isAuthenticated && tenantsQuery.data && tenantsQuery.data.length === 0 && !isAdmin && (
          <Card>
            <div className={styles.empty}>
              <Body1>You don't have access to any tenants yet. Ask an owner to invite you.</Body1>
            </div>
          </Card>
        )}

        {isAuthenticated && tenantsQuery.data && (tenantsQuery.data.length > 0 || isAdmin) && (
          <div className={styles.list}>
            {tenantsQuery.data.map((t) => (
              <Card
                key={t.id}
                className={styles.tenantCard}
                onClick={() => navigate(`/tenants/${t.id}`)}
              >
                <div className={styles.tenantRow}>
                  <div className={styles.iconWrap}>
                    <Building24Regular />
                  </div>
                  <div className={styles.tenantTextStack}>
                    <Body1 className={styles.tenantName}>{t.name}</Body1>
                    <Body1 className={styles.rolesRow}>{t.roles.join(', ')}</Body1>
                  </div>
                  <ChevronRight24Regular style={{ color: tokens.colorNeutralForeground3 }} />
                </div>
              </Card>
            ))}

            {isAdmin && (
              <Card className={styles.createCard} onClick={() => setDialogOpen(true)}>
                <div className={styles.tenantRow}>
                  <div className={styles.createIconWrap}>
                    <Add24Regular />
                  </div>
                  <div className={styles.tenantTextStack}>
                    <Body1 className={styles.createLabel}>Create new tenant</Body1>
                    <Body1 className={styles.rolesRow}>Register a tenant and invite members afterwards</Body1>
                  </div>
                  <ChevronRight24Regular style={{ color: tokens.colorNeutralForeground3 }} />
                </div>
              </Card>
            )}
          </div>
        )}
      </div>

      <RegisterTenantDialog open={dialogOpen} onOpenChange={setDialogOpen} />
    </div>
  );
}
