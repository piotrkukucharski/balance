(function() {
    let rawCsvText = '';
    let parsedRows = [];

    const bankProfiles = {
        'GENERIC': { dateCol: 0, amountCol: 1, payeeCol: 2, refCol: 3, ibanCol: -1 },
        'MBANK': { dateCol: 0, amountCol: 6, payeeCol: 3, refCol: 2, ibanCol: -1 },
        'PKOBP': { dateCol: 0, amountCol: 3, payeeCol: 5, refCol: 7, ibanCol: 6 },
        'SANTANDER': { dateCol: 0, amountCol: 5, payeeCol: 2, refCol: 3, ibanCol: 4 },
        'ING': { dateCol: 0, amountCol: 8, payeeCol: 2, refCol: 3, ibanCol: 5 },
        'MILLENNIUM': { dateCol: 1, amountCol: -1, payeeCol: 2, refCol: 6, ibanCol: -1 }
    };

    window.previewCSVFile = function(input) {
        if (!input.files || input.files.length === 0) return;
        const file = input.files[0];
        const reader = new FileReader();

        reader.onload = function(e) {
            rawCsvText = e.target.result;
            processPreview();
        };

        reader.readAsText(file);
    };

    function processPreview() {
        if (!rawCsvText) return;

        // Sniff delimiter
        let delimiter = ';';
        const sample = rawCsvText.substring(0, 4096);
        const commaCount = (sample.match(/,/g) || []).length;
        const semiCount = (sample.match(/;/g) || []).length;
        const tabCount = (sample.match(/\t/g) || []).length;

        if (commaCount > semiCount && commaCount > tabCount) delimiter = ',';
        else if (tabCount > semiCount && tabCount > commaCount) delimiter = '\t';

        document.getElementById('delimiter-badge').textContent = 'Delimiter: ' + (delimiter === '\t' ? 'TAB' : delimiter);

        // Simple row parsing
        const lines = rawCsvText.split(/\r?\n/).filter(line => line.trim().length > 0);
        if (lines.length === 0) return;

        parsedRows = lines.slice(0, 15).map(line => {
            // Basic split respecting simple quotes
            return line.split(delimiter).map(cell => cell.trim().replace(/^["']|["']$/g, ''));
        });

        if (parsedRows.length === 0) return;

        const maxCols = Math.max(...parsedRows.map(r => r.length));
        populateColumnDropdowns(maxCols, parsedRows[0]);
        renderPreviewTable(parsedRows);

        document.getElementById('preview-card').classList.remove('hidden');

        // Apply profile defaults if selected
        const currentProfile = document.getElementById('bank_profile').value;
        window.applyBankProfile(currentProfile);
    }

    function populateColumnDropdowns(colCount, headerRow) {
        const selects = ['date_col', 'amount_col', 'payee_col', 'reference_col', 'iban_col'];
        selects.forEach(id => {
            const el = document.getElementById(id);
            if (!el) return;
            el.innerHTML = '<option value="-1">-- None --</option>';
            for (let i = 0; i < colCount; i++) {
                const colName = (headerRow && headerRow[i]) ? ('Col ' + (i+1) + ': ' + headerRow[i].substring(0, 16)) : ('Column ' + (i+1));
                const opt = document.createElement('option');
                opt.value = i;
                opt.textContent = colName;
                el.appendChild(opt);
            }
        });
    }

    function renderPreviewTable(rows) {
        const thead = document.getElementById('preview-thead');
        const tbody = document.getElementById('preview-tbody');
        thead.innerHTML = '';
        tbody.innerHTML = '';

        if (rows.length === 0) return;

        const trHead = document.createElement('tr');
        const thIndex = document.createElement('th');
        thIndex.textContent = '#';
        thIndex.className = 'w-8 text-center opacity-60';
        trHead.appendChild(thIndex);

        rows[0].forEach((col, idx) => {
            const th = document.createElement('th');
            th.textContent = 'Col ' + (idx + 1);
            trHead.appendChild(th);
        });
        thead.appendChild(trHead);

        rows.forEach((row, rowIdx) => {
            const tr = document.createElement('tr');
            if (rowIdx === 0) tr.className = 'bg-base-200/40 font-semibold';
            const tdIdx = document.createElement('td');
            tdIdx.textContent = (rowIdx + 1);
            tdIdx.className = 'text-center opacity-60';
            tr.appendChild(tdIdx);

            row.forEach(cell => {
                const td = document.createElement('td');
                td.textContent = cell;
                td.className = 'max-w-xs truncate';
                tr.appendChild(td);
            });
            tbody.appendChild(tr);
        });
    }

    window.applyBankProfile = function(profile) {
        const p = bankProfiles[profile] || bankProfiles['GENERIC'];
        const setVal = (id, val) => {
            const el = document.getElementById(id);
            if (el && val >= -1 && val < el.options.length - 1) {
                el.value = val;
            }
        };
        setVal('date_col', p.dateCol);
        setVal('amount_col', p.amountCol);
        setVal('payee_col', p.payeeCol);
        setVal('reference_col', p.refCol);
        setVal('iban_col', p.ibanCol);
    };
})();
