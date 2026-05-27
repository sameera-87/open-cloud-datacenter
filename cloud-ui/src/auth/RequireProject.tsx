import { Button, Card, Spinner, Text, makeStyles, tokens } from '@fluentui/react-components';
import { useEffect, useReducer, useRef } from 'react';
import { Outlet, useNavigate, useParams } from 'react-router-dom';
import { makeApiClient } from '../api/client';
import { useAuth } from './useAuth';

const useStyles = makeStyles({
  loading: {
    minHeight: '100vh',
    display: 'grid',
    placeItems: 'center',
    backgroundColor: tokens.colorNeutralBackground2,
  },
  errorWrap: {
    minHeight: '100vh',
    display: 'grid',
    placeItems: 'center',
    backgroundColor: tokens.colorNeutralBackground2,
    padding: tokens.spacingHorizontalXXL,
  },
  errorCard: {
    maxWidth: '480px',
    width: '100%',
    padding: `${tokens.spacingVerticalXXL} ${tokens.spacingHorizontalXXL}`,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
    boxShadow: tokens.shadow16,
  },
});

type GateState =
  | { status: 'loading'; attempt: number }
  | { status: 'error'; attempt: number }
  | { status: 'empty'; attempt: number }
  | { status: 'ready'; attempt: number };

type GateAction =
  | { type: 'retry' }
  | { type: 'resolve'; projectCount: number }
  | { type: 'reject' };

function gateReducer(state: GateState, action: GateAction): GateState {
  switch (action.type) {
    case 'retry':
      return { status: 'loading', attempt: state.attempt + 1 };
    case 'resolve':
      return { status: action.projectCount === 0 ? 'empty' : 'ready', attempt: state.attempt };
    case 'reject':
      return { status: 'error', attempt: state.attempt };
  }
}

/**
 * Layout route that gates its children on the tenant having at least one project.
 * Empty → redirect to the ProjectPickerPage (/tenants/:tid).
 * Ready → render the project-scoped Outlet.
 */
export default function RequireProject() {
  const styles = useStyles();
  const { user } = useAuth();
  const { tenantId } = useParams<{ tenantId: string }>();
  const navigate = useNavigate();

  const [gate, dispatch] = useReducer(gateReducer, { status: 'loading', attempt: 0 });
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!user || !tenantId) return;

    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    const client = makeApiClient();

    void client
      .GET('/v1/tenants/{tenant_id}/projects', {
        params: { path: { tenant_id: tenantId } },
        signal: controller.signal,
      } as Parameters<typeof client.GET>[1])
      .then(({ data, response }) => {
        if (controller.signal.aborted) return;
        if (!response.ok) {
          dispatch({ type: 'reject' });
          return;
        }
        dispatch({ type: 'resolve', projectCount: (data ?? []).length });
      })
      .catch(() => {
        if (!controller.signal.aborted) dispatch({ type: 'reject' });
      });

    return () => {
      controller.abort();
    };
  }, [user, tenantId, gate.attempt]);

  if (!user) return null;

  if (gate.status === 'loading') {
    return (
      <div className={styles.loading}>
        <Spinner size="large" label="Loading project…" />
      </div>
    );
  }

  if (gate.status === 'error') {
    return (
      <div className={styles.errorWrap}>
        <Card className={styles.errorCard}>
          <Text weight="semibold" size={500}>Could not load project</Text>
          <Text>There was a problem reaching the API. Check your connection and try again.</Text>
          <Button appearance="primary" onClick={() => dispatch({ type: 'retry' })}>
            Retry
          </Button>
        </Card>
      </div>
    );
  }

  if (gate.status === 'empty') {
    // Redirect to the ProjectPickerPage — it handles the "no projects" CTA.
    navigate(`/tenants/${tenantId}`, { replace: true });
    return null;
  }

  return <Outlet />;
}
