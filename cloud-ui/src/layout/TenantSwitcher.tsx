import {
  Badge,
  Button,
  Menu,
  MenuDivider,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Spinner,
  Tooltip,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import {
  Add16Regular,
  Building20Regular,
  Checkmark16Filled,
  ChevronDown16Regular,
} from '@fluentui/react-icons';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useAuth } from '../auth/useAuth';
import RegisterTenantDialog from '../components/RegisterTenantDialog';

const useStyles = makeStyles({
  trigger: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
  },
  label: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
  },
  name: { fontWeight: 600 },
  menuRow: {
    display: 'flex',
    flexDirection: 'column',
    gap: '2px',
    flex: 1,
    minWidth: 0,
  },
  menuRowName: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  menuRowId: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    fontFamily: tokens.fontFamilyMonospace,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  menuRowBadges: {
    display: 'flex',
    gap: tokens.spacingHorizontalXS,
    flexWrap: 'wrap',
    marginTop: '2px',
  },
  checkIcon: {
    color: tokens.colorBrandForeground1,
    flexShrink: 0,
    width: '16px',
    height: '16px',
  },
  checkPlaceholder: {
    width: '16px',
    flexShrink: 0,
  },
  menuItem: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    paddingTop: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalS,
    width: '260px',
  },
  createRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    color: tokens.colorBrandForeground1,
    fontWeight: tokens.fontWeightSemibold,
  },
  emptyMsg: {
    padding: `${tokens.spacingVerticalM} ${tokens.spacingHorizontalL}`,
    maxWidth: '260px',
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
    lineHeight: tokens.lineHeightBase300,
  },
});

type TenantRole = 'owner' | 'member' | 'viewer';

function RoleBadge({ role }: { role: TenantRole }) {
  if (role === 'owner') {
    return (
      <Badge appearance="filled" color="brand" size="small">
        owner
      </Badge>
    );
  }
  if (role === 'member') {
    return (
      <Badge appearance="ghost" color="success" size="small">
        member
      </Badge>
    );
  }
  return (
    <Badge appearance="outline" color="informative" size="small">
      viewer
    </Badge>
  );
}

export default function TenantSwitcher() {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId } = useParams<{ tenantId: string }>();
  const { user } = useAuth();
  const isAdmin = user?.isAdmin ?? false;
  const [dialogOpen, setDialogOpen] = useState(false);

  const tenantsQuery = useQuery({
    queryKey: ['tenants'],
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data ?? [];
    },
  });

  const tenants = tenantsQuery.data ?? [];
  const activeTenant = tenants.find((t) => t.id === tenantId);

  const onSelect = (id: string) => {
    if (id !== tenantId) navigate(`/tenants/${id}`);
  };

  // Loading state: show spinner inside the trigger button
  if (tenantsQuery.isLoading) {
    return (
      <Button appearance="subtle" icon={<Building20Regular />} disabled>
        <div className={styles.trigger}>
          <span className={styles.label}>Tenant</span>
          <Spinner size="tiny" />
        </div>
      </Button>
    );
  }

  // Whether to render the menu (vs the bare static label). Always for >1 tenant
  // OR when the user is an admin (so they can reach the Create tenant entry).
  const showMenu = tenants.length > 1 || isAdmin;

  // Single-tenant, non-admin: non-interactive label — no menu, no chevron
  if (!showMenu) {
    const sole = tenants[0];
    return (
      <Button appearance="subtle" icon={<Building20Regular />} disabled>
        <div className={styles.trigger}>
          <span className={styles.label}>Tenant</span>
          <span className={styles.name}>{sole?.name ?? sole?.id ?? '—'}</span>
        </div>
      </Button>
    );
  }

  const triggerLabel =
    activeTenant?.name ??
    activeTenant?.id ??
    tenants[0]?.name ??
    tenants[0]?.id ??
    tenantId ??
    'Select tenant';

  return (
    <>
      <Menu>
        <MenuTrigger disableButtonEnhancement>
          <Button appearance="subtle" icon={<Building20Regular />}>
            <div className={styles.trigger}>
              <span className={styles.label}>Tenant</span>
              <span className={styles.name}>{triggerLabel}</span>
              <ChevronDown16Regular />
            </div>
          </Button>
        </MenuTrigger>

        <MenuPopover>
          <MenuList>
            {tenantsQuery.isError && (
              <MenuItem disabled>Failed to load tenants</MenuItem>
            )}

            {!tenantsQuery.isError && tenants.length === 0 && (
              <div className={styles.emptyMsg}>
                {isAdmin
                  ? 'No tenants yet. Create the first one below.'
                  : "You don't have access to any tenants yet. Ask an owner to invite you."}
              </div>
            )}

            {tenants.map((t) => {
              const isActive = t.id === tenantId;
              const displayName = t.name ?? t.id;
              const showId = t.name && t.name !== t.id;
              const roles = (t.roles ?? []) as TenantRole[];

              return (
                <MenuItem key={t.id} onClick={() => onSelect(t.id)}>
                  <div className={styles.menuItem}>
                    {isActive ? (
                      <Checkmark16Filled className={styles.checkIcon} />
                    ) : (
                      <span className={styles.checkPlaceholder} />
                    )}
                    <div className={styles.menuRow}>
                      <span className={styles.menuRowName}>{displayName}</span>
                      {showId && (
                        <Tooltip content={t.id} relationship="label" withArrow positioning="below-start">
                          <span className={styles.menuRowId}>{t.id}</span>
                        </Tooltip>
                      )}
                      {roles.length > 0 && (
                        <div className={styles.menuRowBadges}>
                          {roles.map((r) => (
                            <RoleBadge key={r} role={r} />
                          ))}
                        </div>
                      )}
                    </div>
                  </div>
                </MenuItem>
              );
            })}

            {isAdmin && (
              <>
                {tenants.length > 0 && <MenuDivider />}
                <MenuItem onClick={() => setDialogOpen(true)}>
                  <div className={styles.createRow}>
                    <Add16Regular />
                    Create tenant
                  </div>
                </MenuItem>
              </>
            )}
          </MenuList>
        </MenuPopover>
      </Menu>

      <RegisterTenantDialog open={dialogOpen} onOpenChange={setDialogOpen} />
    </>
  );
}
