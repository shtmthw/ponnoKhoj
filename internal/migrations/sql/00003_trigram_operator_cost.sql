-- +goose Up
-- pg_trgm declares its similarity operator functions with the default
-- planner cost of 1, i.e. as cheap as an integer comparison. Evaluating one
-- actually takes about 3 microseconds per row, so the planner prefers to scan
-- every product over using the GIN trigram indexes. Measured on 20,030
-- products: 85-104 ms sequential vs 17 ms via the indexes (plans in
-- docs/architecture.md). COST 100 tells the planner the truth.
--
-- Only the functions' owner may change their cost. Locally and in the
-- integration tests the migrating role created the extension and owns them.
-- On CloudNativePG a superuser creates the extension at bootstrap and runs
-- the same ALTER statements there (deploy/k8s/base/postgres.yaml), so here
-- the migration only notes that it skipped them. Search is correct either
-- way; only its speed depends on this.
-- +goose StatementBegin
DO $$
DECLARE
    fn regprocedure;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'similarity_op(text,text)'::regprocedure,
        'word_similarity_op(text,text)'::regprocedure,
        'word_similarity_commutator_op(text,text)'::regprocedure]
    LOOP
        IF pg_has_role((SELECT proowner FROM pg_proc WHERE oid = fn), 'USAGE') THEN
            EXECUTE format('ALTER FUNCTION %s COST 100', fn);
        ELSE
            RAISE NOTICE 'not the owner of %; its planner cost is set by the database bootstrap instead', fn;
        END IF;
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
    fn regprocedure;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'similarity_op(text,text)'::regprocedure,
        'word_similarity_op(text,text)'::regprocedure,
        'word_similarity_commutator_op(text,text)'::regprocedure]
    LOOP
        IF pg_has_role((SELECT proowner FROM pg_proc WHERE oid = fn), 'USAGE') THEN
            EXECUTE format('ALTER FUNCTION %s COST 1', fn);
        END IF;
    END LOOP;
END
$$;
-- +goose StatementEnd
