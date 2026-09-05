-- +goose Up
-- Guarda as etapas que um passo do SDK anuncia enquanto roda.
--
-- Ate aqui um passo do SDK era uma caixa cinza que virava verde. Entre
-- "comecou" e "acabou" havia quarenta minutos em que a tela nao distinguia
-- "baixando a pagina 300 de 4.803" de "travado no handshake do Redshift".
--
-- JSONB numa coluna, e nao uma tabela propria: sao no maximo quatro registros
-- de ~60 bytes por tentativa, sempre lidos junto com a linha pai e nunca
-- consultados sozinhos. E task_runs ja e chaveada por (run_id, node_id,
-- attempt), entao as etapas ficam por tentativa sem FK nova e sem join novo.
--
-- Quem escreve aplica um teto; ver `tetoDeEtapas` no SDK e o coletor no runner.
ALTER TABLE task_runs ADD COLUMN etapas JSONB NOT NULL DEFAULT '[]'::jsonb;

-- A versao do SDK que o passo anunciou, vazia para um passo que nao e do SDK.
--
-- Ela vem do proprio binario (runtime/debug), nao do YAML: ninguem digita e
-- ninguem mantem em sincronia, entao o selo na tela nao tem como mentir.
ALTER TABLE task_runs ADD COLUMN sdk_versao TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE task_runs DROP COLUMN sdk_versao;
ALTER TABLE task_runs DROP COLUMN etapas;
